package acme

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// directory is the parsed RFC 8555 §7.1.1 directory document. Only the
// four endpoints we drive are kept; the rest of the document is stored
// in raw so future fields are accessible without a re-fetch.
type directory struct {
	NewNonce   string `json:"newNonce"`
	NewAccount string `json:"newAccount"`
	NewOrder   string `json:"newOrder"`
	RevokeCert string `json:"revokeCert"`
	KeyChange  string `json:"keyChange"`
}

// problem is the RFC 7807 problem document the ACME server returns on
// errors (RFC 8555 §6.7). The Type field carries the urn:ietf:params:
// acme:error:* identifier we switch on for retry classification.
type problem struct {
	Type        string `json:"type"`
	Detail      string `json:"detail"`
	Status      int    `json:"status"`
	Subproblems []struct {
		Type       string `json:"type"`
		Detail     string `json:"detail"`
		Identifier struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"identifier"`
	} `json:"subproblems,omitempty"`
}

func (p *problem) Error() string {
	if p == nil {
		return "<nil acme problem>"
	}
	if p.Detail != "" {
		return fmt.Sprintf("acme problem %s (status %d): %s", p.Type, p.Status, p.Detail)
	}
	return fmt.Sprintf("acme problem %s (status %d)", p.Type, p.Status)
}

// nonceCache is the supervisor's pool of fresh Replay-Nonce values. The
// ACME server returns a fresh nonce on every response (RFC 8555 §6.5);
// we cache them so an authenticated POST does not have to round-trip to
// newNonce first. The cache holds at most one nonce because nonces are
// single-use; a deeper pool would just hold soon-to-expire values.
type nonceCache struct {
	mu    sync.Mutex
	value string
}

func (n *nonceCache) take() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	v := n.value
	n.value = ""
	return v
}

func (n *nonceCache) put(v string) {
	if v == "" {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.value = v
}

// fetchDirectory retrieves and decodes the ACME directory document.
func (c *Client) fetchDirectory(ctx context.Context) (*directory, error) {
	if c.dir != nil {
		return c.dir, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.opts.DirectoryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("acme: build directory request: %w", err)
	}
	req.Header.Set("User-Agent", c.opts.UserAgent)
	resp, err := c.opts.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("acme: GET directory: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("acme: read directory: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("acme: directory status %d: %s", resp.StatusCode, string(body))
	}
	d := &directory{}
	if err := json.Unmarshal(body, d); err != nil {
		return nil, fmt.Errorf("acme: decode directory: %w", err)
	}
	c.dir = d
	return d, nil
}

// fetchNonce hits newNonce and caches the returned Replay-Nonce.
func (c *Client) fetchNonce(ctx context.Context) (string, error) {
	if v := c.nonces.take(); v != "" {
		return v, nil
	}
	dir, err := c.fetchDirectory(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, dir.NewNonce, nil)
	if err != nil {
		return "", fmt.Errorf("acme: build newNonce request: %w", err)
	}
	req.Header.Set("User-Agent", c.opts.UserAgent)
	resp, err := c.opts.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("acme: HEAD newNonce: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	v := resp.Header.Get("Replay-Nonce")
	if v == "" {
		return "", errors.New("acme: newNonce returned no Replay-Nonce")
	}
	return v, nil
}

// rawResponse is the structured outcome of a low-level ACME POST. Body
// may be empty if the response was 204; Location carries the resource
// URL the server assigned (e.g. the order URL).
type rawResponse struct {
	Status   int
	Body     []byte
	Location string
	Header   http.Header
}

// post issues a JWS-authenticated POST. payload may be nil for
// POST-as-GET. signer is required; kid may be empty for newAccount, in
// which case the JWS embeds the JWK. result, when non-nil, is decoded
// from the response body as JSON.
func (c *Client) post(ctx context.Context, signer jwsSigner, kid, url string, payload any, result any) (*rawResponse, error) {
	var payloadBytes []byte
	if payload != nil {
		switch v := payload.(type) {
		case []byte:
			payloadBytes = v
		case json.RawMessage:
			payloadBytes = []byte(v)
		default:
			b, err := json.Marshal(payload)
			if err != nil {
				return nil, fmt.Errorf("acme: marshal payload: %w", err)
			}
			payloadBytes = b
		}
	} else {
		// POST-as-GET: payload is the empty string per RFC 8555 §6.3.
		payloadBytes = []byte("")
	}
	// Retry up to maxAttempts on badNonce; every other retryable problem
	// is surfaced for the caller's exponential-backoff harness.
	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		nonce, err := c.fetchNonce(ctx)
		if err != nil {
			return nil, err
		}
		jws, err := signRequest(signer, kid, url, nonce, payloadBytes)
		if err != nil {
			return nil, err
		}
		body, err := json.Marshal(jws)
		if err != nil {
			return nil, fmt.Errorf("acme: marshal jws: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("acme: build POST: %w", err)
		}
		req.Header.Set("Content-Type", "application/jose+json")
		req.Header.Set("User-Agent", c.opts.UserAgent)
		resp, err := c.opts.HTTPClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("acme: POST %s: %w", url, err)
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if v := resp.Header.Get("Replay-Nonce"); v != "" {
			c.nonces.put(v)
		}
		if resp.StatusCode >= 400 {
			p := &problem{Status: resp.StatusCode}
			if len(respBody) > 0 {
				_ = json.Unmarshal(respBody, p)
			}
			if p.Type == "urn:ietf:params:acme:error:badNonce" && attempt+1 < maxAttempts {
				lastErr = p
				continue
			}
			return &rawResponse{
				Status:   resp.StatusCode,
				Body:     respBody,
				Location: resp.Header.Get("Location"),
				Header:   resp.Header.Clone(),
			}, p
		}
		if result != nil && len(respBody) > 0 {
			if err := json.Unmarshal(respBody, result); err != nil {
				return nil, fmt.Errorf("acme: decode response from %s: %w", url, err)
			}
		}
		return &rawResponse{
			Status:   resp.StatusCode,
			Body:     respBody,
			Location: resp.Header.Get("Location"),
			Header:   resp.Header.Clone(),
		}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("acme: post exhausted retries")
	}
	return nil, lastErr
}
