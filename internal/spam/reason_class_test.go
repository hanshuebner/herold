package spam

// reason_class_test.go covers the reason-class taxonomy (re #326): every
// Unclassified outcome Classify returns carries both a non-nil error a
// caller can pass to ReasonClass, and a human-readable Reason string
// ("<class>: <error text>") a caller can persist or log directly without
// re-deriving the class itself.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

func TestReasonClass_Table(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"not configured", ErrNotConfigured, "not_configured"},
		{"context deadline", context.DeadlineExceeded, "timeout"},
		{"context cancelled", context.Canceled, "timeout"},
		{"rpc deadline marker", errors.New("json-rpc error -32001: rpc deadline exceeded"), "timeout"},
		{"unparseable verdict", ErrUnparseableVerdict, "unparseable"},
		{"nil response", ErrNilResponse, "unparseable"},
		{"generic plugin error", errors.New("plugin crashed"), "plugin_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReasonClass(tc.err); got != tc.want {
				t.Errorf("ReasonClass(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestClassify_UnclassifiedReasonPopulated asserts every Classify path
// that returns Unclassified also populates Classification.Reason with
// the "<class>: <detail>" string (re #326 Expected: the INFO log line
// and the persisted llm_classifications row both need this without
// re-deriving it from the raw error at every call site).
func TestClassify_UnclassifiedReasonPopulated(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		c := New(nil, silentLogger(), clock.NewFake(time.Now()))
		r, err := c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "any", ClassifyContext{})
		if !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("err = %v, want ErrNotConfigured", err)
		}
		if !strings.HasPrefix(r.Reason, "not_configured: ") {
			t.Fatalf("Reason = %q, want not_configured prefix", r.Reason)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		invoker := newFakeInvoker()
		invoker.handle("slow", ClassifyMethod, func(ctx context.Context, _ any) (json.RawMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
		c := New(invoker, silentLogger(), clock.NewFake(time.Now()))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		r, err := c.Classify(ctx, buildMessage(t, canonMsg), nil, "slow", ClassifyContext{})
		if err == nil {
			t.Fatal("expected timeout error")
		}
		if !strings.HasPrefix(r.Reason, "timeout: ") {
			t.Fatalf("Reason = %q, want timeout prefix", r.Reason)
		}
	})

	t.Run("plugin error", func(t *testing.T) {
		invoker := newFakeInvoker()
		invoker.handle("broken", ClassifyMethod, func(_ context.Context, _ any) (json.RawMessage, error) {
			return nil, errors.New("plugin crashed")
		})
		c := New(invoker, silentLogger(), clock.NewFake(time.Now()))
		r, err := c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "broken", ClassifyContext{})
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.HasPrefix(r.Reason, "plugin_error: ") {
			t.Fatalf("Reason = %q, want plugin_error prefix", r.Reason)
		}
	})

	t.Run("unparseable", func(t *testing.T) {
		invoker := newFakeInvoker()
		invoker.handle("odd", ClassifyMethod, func(_ context.Context, _ any) (json.RawMessage, error) {
			return json.RawMessage(`{"verdict":"maybe","confidence":0.5}`), nil
		})
		c := New(invoker, silentLogger(), clock.NewFake(time.Now()))
		r, err := c.Classify(context.Background(), buildMessage(t, canonMsg), nil, "odd", ClassifyContext{})
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.HasPrefix(r.Reason, "unparseable: ") {
			t.Fatalf("Reason = %q, want unparseable prefix", r.Reason)
		}
	})
}
