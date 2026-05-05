package load

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// InboundBurstScenario drives N concurrent SMTP connections each delivering
// M messages within a timeout, then verifies that all messages landed in the
// store.
//
// Gate thresholds (full-scale):
//
//   - delivered_messages >= N*M
//   - error_rate         <= 0.01  (1 %)
//   - duration_seconds   <= 60
//
// The smoke-test variant (N=2, M=5) exercises the same code path but
// completes in seconds and is safe to run in unit tests / CI.
type InboundBurstScenario struct {
	// Connections is the number of concurrent SMTP connections.
	// Full-scale per 03-testing-strategy.md: 500.
	Connections int
	// MessagesPerConn is the number of messages each connection delivers.
	// Full-scale: 10 000.
	MessagesPerConn int
	// TimeoutSeconds is the wall-time budget for all deliveries.
	// Full-scale: 60.  Smoke test: 30.
	TimeoutSeconds int
	// MaxErrorRate is the acceptable fraction of failed sends (0.0–1.0).
	MaxErrorRate float64
}

// Name implements Scenario.
func (s *InboundBurstScenario) Name() string { return "inbound_burst" }

// Run implements Scenario.
func (s *InboundBurstScenario) Run(ctx context.Context, h *Harness) *RunResult {
	r, start := newRunResult(s.Name(), h.Backend)

	conns := s.Connections
	if conns <= 0 {
		conns = 2
	}
	msgsPerConn := s.MessagesPerConn
	if msgsPerConn <= 0 {
		msgsPerConn = 5
	}
	timeoutSecs := s.TimeoutSeconds
	if timeoutSecs <= 0 {
		timeoutSecs = 30
	}
	maxErrRate := s.MaxErrorRate
	if maxErrRate <= 0 {
		maxErrRate = 0.01
	}

	totalExpected := conns * msgsPerConn

	// Create one recipient principal in the harness domain.
	recvCtx, recvCancel := context.WithTimeout(ctx, 30*time.Second)
	defer recvCancel()
	_, err := h.CreatePrincipal(recvCtx, "burst-recv", "burst-password-01!")
	if err != nil {
		r.addError(fmt.Errorf("create principal: %w", err))
		r.finish(start)
		return r
	}
	recipient := "burst-recv@" + h.Domain
	sender := "burst-sender@external.test"

	runCtx, runCancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
	defer runCancel()

	var (
		delivered  int64
		errCount   int64
		mu         sync.Mutex
		errSamples []string
	)

	var wg sync.WaitGroup
	for i := 0; i < conns; i++ {
		wg.Add(1)
		go func(connIdx int) {
			defer wg.Done()

			c, err := dialSMTP(runCtx, h.SMTPAddr)
			if err != nil {
				atomic.AddInt64(&errCount, 1)
				mu.Lock()
				if len(errSamples) < 10 {
					errSamples = append(errSamples, fmt.Sprintf("conn %d dial: %v", connIdx, err))
				}
				mu.Unlock()
				return
			}
			defer c.quit()

			for j := 0; j < msgsPerConn; j++ {
				if runCtx.Err() != nil {
					break
				}
				body := fmt.Sprintf(
					"From: %s\r\nTo: %s\r\nSubject: load-burst-%d-%d\r\n"+
						"Date: Mon, 01 Jan 2026 00:00:00 +0000\r\n"+
						"Message-ID: <burst-%d-%d@load.test>\r\n\r\n"+
						"load test message body conn=%d msg=%d\r\n",
					sender, recipient, connIdx, j,
					connIdx, j, connIdx, j,
				)
				if err := c.deliverMessage(sender, recipient, body); err != nil {
					atomic.AddInt64(&errCount, 1)
					mu.Lock()
					if len(errSamples) < 10 {
						errSamples = append(errSamples, fmt.Sprintf("conn %d msg %d: %v", connIdx, j, err))
					}
					mu.Unlock()
					// Reconnect on error so subsequent messages have a clean session.
					c.close()
					newC, dialErr := dialSMTP(runCtx, h.SMTPAddr)
					if dialErr != nil {
						return
					}
					c = newC
				} else {
					atomic.AddInt64(&delivered, 1)
				}
			}
		}(i)
	}

	wg.Wait()
	dur := time.Since(start)

	deliveredN := atomic.LoadInt64(&delivered)
	errorN := atomic.LoadInt64(&errCount)
	total := float64(deliveredN + errorN)
	var errorRate float64
	if total > 0 {
		errorRate = float64(errorN) / total
	}

	r.Metrics["connections"] = float64(conns)
	r.Metrics["messages_per_conn"] = float64(msgsPerConn)
	r.Metrics["messages_expected"] = float64(totalExpected)
	r.Metrics["messages_delivered"] = float64(deliveredN)
	r.Metrics["error_count"] = float64(errorN)
	r.Metrics["error_rate"] = errorRate
	r.Metrics["duration_seconds"] = dur.Seconds()
	if dur.Seconds() > 0 {
		r.Metrics["throughput_msg_per_sec"] = float64(deliveredN) / dur.Seconds()
	}

	for _, es := range errSamples {
		r.addError(fmt.Errorf("%s", es))
	}

	r.addGateGTE("messages_delivered", float64(totalExpected), float64(deliveredN))
	r.addGateLTE("error_rate", maxErrRate, errorRate)
	r.addGateLTE("duration_seconds", float64(timeoutSecs), dur.Seconds())

	if d := h.StopPprof(); d != "" {
		r.PprofDir = d
	}
	r.finish(start)
	return r
}
