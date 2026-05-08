package gmail

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIsBusyError(t *testing.T) {
	cases := map[string]bool{
		"":                                       false,
		"normal failure":                         false,
		"database is locked":                     true,
		"database is locked (5) (SQLITE_BUSY)":   true,
		"database is locked (517)":               true,
		"insert message: database is locked (5)": true,
	}
	for s, want := range cases {
		var err error
		if s != "" {
			err = errors.New(s)
		}
		if got := isBusyError(err); got != want {
			t.Errorf("isBusyError(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestRetryOnBusyRetriesThenSucceeds(t *testing.T) {
	ctx := context.Background()
	calls := 0
	err := retryOnBusy(ctx, "test op", func() error {
		calls++
		if calls < 3 {
			return errors.New("database is locked (517)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryOnBusy: %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestRetryOnBusyPassesThroughNonBusyErrors(t *testing.T) {
	ctx := context.Background()
	want := errors.New("permanent failure")
	err := retryOnBusy(ctx, "test op", func() error { return want })
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestRetryOnBusyHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := retryOnBusy(ctx, "test op", func() error {
		calls++
		return errors.New("database is locked")
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if calls < 1 {
		t.Errorf("calls = %d, expected at least 1", calls)
	}
}
