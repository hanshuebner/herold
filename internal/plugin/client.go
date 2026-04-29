package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/semaphore"

	"github.com/hanshuebner/herold/internal/observe"
)

// NotificationHandler receives plugin-to-server notifications (log, metric,
// notify). It must not block on the caller's goroutine; supervisors route
// into bounded channels or direct slog/metrics calls.
type NotificationHandler interface {
	OnLog(params LogParams)
	OnMetric(params MetricParams)
	OnNotify(params NotifyParams)
	OnUnknown(method string, params json.RawMessage)
}

// Client is the supervisor-side JSON-RPC 2.0 client for one plugin. It
// owns the stdio read loop, the outstanding-request table, and the
// concurrency semaphore.
//
// A Client is usable only while Run is executing. Run exits when the reader
// returns an error (EOF on plugin crash) or when Close is called.
type Client struct {
	name   string
	logger *slog.Logger

	in  *FrameReader
	out *FrameWriter

	sem *semaphore.Weighted

	mu       sync.Mutex
	pending  map[string]chan *Response
	closed   bool
	closeErr error

	idCounter atomic.Int64

	notif NotificationHandler
}

// ClientOptions configures a Client. Zero values pick safe defaults.
type ClientOptions struct {
	Name          string
	Logger        *slog.Logger
	MaxConcurrent int64
	MaxFrameBytes int
	Notifications NotificationHandler
}

// NewClient wires a Client to the given reader/writer pair. Typical callers
// pass a child process's stdout and stdin. Run must be invoked to drive the
// read loop.
func NewClient(r io.Reader, w io.Writer, opts ClientOptions) *Client {
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 1
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Client{
		name:    opts.Name,
		logger:  opts.Logger.With("plugin", opts.Name),
		in:      NewFrameReader(r, opts.MaxFrameBytes),
		out:     NewFrameWriter(w),
		sem:     semaphore.NewWeighted(opts.MaxConcurrent),
		pending: make(map[string]chan *Response),
		notif:   opts.Notifications,
	}
}

// SetMaxConcurrent replaces the semaphore with one sized for n (post-
// handshake, the supervisor learns the plugin's declared limit).
func (c *Client) SetMaxConcurrent(n int64) {
	if n <= 0 {
		n = 1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sem = semaphore.NewWeighted(n)
}

// Run drives the read loop until the input returns an error or Close is
// called. It is safe to call Run once; subsequent calls return
// errClientClosed.
func (c *Client) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			c.closeWithError(err)
			return err
		}
		raw, err := c.in.ReadFrame()
		if err != nil {
			c.closeWithError(err)
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		c.dispatch(raw)
	}
}

func (c *Client) dispatch(raw []byte) {
	kind, err := ClassifyFrame(raw)
	if err != nil {
		c.logger.Warn("plugin frame classify failed",
			"activity", observe.ActivityInternal,
			"err", err,
			"len", len(raw))
		return
	}
	switch kind {
	case FrameResponse:
		var resp Response
		if err := json.Unmarshal(raw, &resp); err != nil {
			c.logger.Warn("plugin response decode failed",
				"activity", observe.ActivityInternal,
				"err", err)
			return
		}
		c.deliverResponse(&resp)
	case FrameRequest, FrameNotification:
		var req Request
		if err := json.Unmarshal(raw, &req); err != nil {
			c.logger.Warn("plugin request decode failed",
				"activity", observe.ActivityInternal,
				"err", err)
			return
		}
		c.handleIncoming(&req)
	default:
		c.logger.Warn("plugin unknown frame",
			"activity", observe.ActivityInternal,
			"len", len(raw))
	}
}

func (c *Client) handleIncoming(req *Request) {
	// Plugin-to-server surface is limited to log/metric/notify notifications.
	// Requests from the plugin are rejected; we do not expose state access.
	if c.notif == nil {
		if req.ID != nil {
			_ = c.out.WriteFrame(Response{
				JSONRPC: JSONRPCVersion,
				ID:      req.ID,
				Error: &Error{
					Code:    ErrCodeMethodNotFound,
					Message: "plugin-to-server requests are not supported",
				},
			})
		}
		return
	}
	switch req.Method {
	case MethodLog:
		var p LogParams
		_ = json.Unmarshal(req.Params, &p)
		c.notif.OnLog(p)
	case MethodMetric:
		var p MetricParams
		_ = json.Unmarshal(req.Params, &p)
		c.notif.OnMetric(p)
	case MethodNotify:
		var p NotifyParams
		_ = json.Unmarshal(req.Params, &p)
		c.notif.OnNotify(p)
	default:
		c.notif.OnUnknown(req.Method, req.Params)
		if req.ID != nil {
			_ = c.out.WriteFrame(Response{
				JSONRPC: JSONRPCVersion,
				ID:      req.ID,
				Error: &Error{
					Code:    ErrCodeMethodNotFound,
					Message: "unknown plugin-to-server method: " + req.Method,
				},
			})
		}
	}
}

func (c *Client) deliverResponse(resp *Response) {
	key := string(resp.ID)
	c.mu.Lock()
	ch, ok := c.pending[key]
	if ok {
		delete(c.pending, key)
	}
	c.mu.Unlock()
	if !ok {
		c.logger.Warn("plugin response without pending request",
			"activity", observe.ActivityInternal,
			"id", key)
		return
	}
	ch <- resp
}

// Call issues a request and waits for the matching response. The ctx deadline
// is enforced; on timeout the pending slot is freed and ErrCodeTimeout is
// returned.
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	if err := c.sem.Acquire(ctx, 1); err != nil {
		return err
	}
	defer c.sem.Release(1)

	id := c.nextID()
	idJSON, _ := json.Marshal(id)

	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("plugin: marshal params: %w", err)
		}
		raw = b
	}

	ch := make(chan *Response, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		if c.closeErr != nil {
			return c.closeErr
		}
		return errClientClosed
	}
	c.pending[string(idJSON)] = ch
	c.mu.Unlock()

	req := Request{
		JSONRPC: JSONRPCVersion,
		ID:      idJSON,
		Method:  method,
		Params:  raw,
	}
	if err := c.out.WriteFrame(req); err != nil {
		c.mu.Lock()
		delete(c.pending, string(idJSON))
		c.mu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, string(idJSON))
		c.mu.Unlock()
		// Best-effort cancel notification to the plugin.
		_ = c.Notify(context.Background(), MethodCancel, CancelParams{ID: idJSON})
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &Error{Code: ErrCodeTimeout, Message: "rpc deadline exceeded"}
		}
		return ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("plugin: decode result: %w", err)
			}
		}
		return nil
	}
}

// Notify sends a JSON-RPC notification (no ID, no response expected).
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("plugin: marshal notify params: %w", err)
		}
		raw = b
	}
	return c.out.WriteFrame(Request{
		JSONRPC: JSONRPCVersion,
		Method:  method,
		Params:  raw,
	})
}

// Close marks the client closed; all pending Calls wake up with
// errClientClosed.
func (c *Client) Close() {
	c.closeWithError(errClientClosed)
}

var errClientClosed = errors.New("plugin: client closed")

func (c *Client) closeWithError(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.closeErr = err
	pending := c.pending
	c.pending = map[string]chan *Response{}
	c.mu.Unlock()
	for id, ch := range pending {
		resp := &Response{
			JSONRPC: JSONRPCVersion,
			ID:      json.RawMessage(id),
			Error: &Error{
				Code:    ErrCodeUnavailable,
				Message: "plugin connection closed: " + err.Error(),
			},
		}
		select {
		case ch <- resp:
		default:
		}
	}
}

func (c *Client) nextID() int64 {
	return c.idCounter.Add(1)
}

// EncodeID renders an int64 request ID as a JSON number string, exposed for
// tests that want to correlate frames without going through Call.
func EncodeID(id int64) string { return strconv.FormatInt(id, 10) }
