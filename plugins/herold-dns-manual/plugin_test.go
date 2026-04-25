package main_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

// The manual plugin has no provider HTTP server: the "provider" is the
// operator. dns.present writes a JSON descriptor to output_path; the
// operator (here, a test goroutine) deletes the file to acknowledge.

type spawnedPlugin struct {
	t      *testing.T
	cmd    *exec.Cmd
	client *plug.Client
	done   chan error
}

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

func buildPluginBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "herold-dns-manual-bin-")
		if err != nil {
			binErr = err
			return
		}
		bin := filepath.Join(dir, "herold-dns-manual")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, "github.com/hanshuebner/herold/plugins/herold-dns-manual")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("go build: %v\n%s", err, out)
			return
		}
		binPath = bin
	})
	if binErr != nil {
		t.Fatalf("build plugin: %v", binErr)
	}
	return binPath
}

func spawnPlugin(t *testing.T, configureOpts map[string]any) *spawnedPlugin {
	t.Helper()
	bin := buildPluginBinary(t)

	cmd := exec.Command(bin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	client := plug.NewClient(stdout, stdin, plug.ClientOptions{
		Name:          "herold-dns-manual",
		MaxConcurrent: 4,
	})
	done := make(chan error, 1)
	go func() { done <- client.Run(context.Background()) }()

	sp := &spawnedPlugin{t: t, cmd: cmd, client: client, done: done}

	{
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var res plug.InitializeResult
		if err := client.Call(ctx, plug.MethodInitialize, plug.InitializeParams{
			ServerVersion: "test",
			ABIVersion:    plug.ABIVersion,
		}, &res); err != nil {
			sp.close()
			t.Fatalf("initialize: %v", err)
		}
	}
	if configureOpts != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var res plug.ConfigureResult
		if err := client.Call(ctx, plug.MethodConfigure, plug.ConfigureParams{Options: configureOpts}, &res); err != nil {
			sp.close()
			t.Fatalf("configure: %v", err)
		}
	}
	t.Cleanup(sp.close)
	return sp
}

func (s *spawnedPlugin) close() {
	if p, ok := s.cmd.Stdin.(io.Closer); ok {
		_ = p.Close()
	}
	waited := make(chan error, 1)
	go func() { waited <- s.cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-waited
	}
}

// presentAsync drives dns.present in a goroutine and returns a channel
// callers can wait on. The test typically deletes the descriptor file
// in the meantime so the plugin returns success.
func (s *spawnedPlugin) presentAsync(t *testing.T, in sdk.DNSPresentParams, timeout time.Duration) <-chan presentReply {
	t.Helper()
	ch := make(chan presentReply, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		var res sdk.DNSPresentResult
		err := s.client.Call(ctx, sdk.MethodDNSPresent, in, &res)
		ch <- presentReply{Result: res, Err: err}
	}()
	return ch
}

type presentReply struct {
	Result sdk.DNSPresentResult
	Err    error
}

func (s *spawnedPlugin) cleanupAsync(t *testing.T, id string, timeout time.Duration) <-chan error {
	t.Helper()
	ch := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		var res map[string]any
		ch <- s.client.Call(ctx, sdk.MethodDNSCleanup, sdk.DNSCleanupParams{ID: id}, &res)
	}()
	return ch
}

func (s *spawnedPlugin) list(t *testing.T, in sdk.DNSListParams) ([]sdk.DNSRecord, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var res []sdk.DNSRecord
	err := s.client.Call(ctx, sdk.MethodDNSList, in, &res)
	return res, err
}

// waitForFile spins until the descriptor file appears or the deadline
// elapses. The plugin writes the file synchronously inside dns.present
// before it starts polling, so the wait is short in practice.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file %s did not appear within %s", path, timeout)
}

// configuredOpts returns the standard options pointing at a per-test
// temp dir with a sub-second poll cadence so the test path is fast.
func configuredOpts(t *testing.T) (map[string]any, string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "pending.json")
	return map[string]any{
		"output_path":             out,
		"confirm_timeout_seconds": 30,
		"poll_interval_seconds":   0.05,
	}, out
}

func TestPresent_RecordTypes(t *testing.T) {
	opts, outPath := configuredOpts(t)
	p := spawnPlugin(t, opts)

	cases := []struct {
		recordType string
		name       string
		value      string
	}{
		{"TXT", "_acme-challenge.example.com", "abc123"},
		{"A", "host.example.com", "192.0.2.1"},
		{"AAAA", "host.example.com", "2001:db8::1"},
		{"MX", "example.com", "10 mail.example.com"},
		{"TLSA", "_25._tcp.mail.example.com", "3 1 1 abcd"},
	}
	for _, tc := range cases {
		t.Run(tc.recordType, func(t *testing.T) {
			ch := p.presentAsync(t, sdk.DNSPresentParams{
				Zone:       "example.com",
				RecordType: tc.recordType,
				Name:       tc.name,
				Value:      tc.value,
				TTL:        300,
			}, 10*time.Second)

			waitForFile(t, outPath, 5*time.Second)
			// Inspect the file before deleting so we can assert its shape.
			data, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("read descriptor: %v", err)
			}
			var rec map[string]any
			if err := json.Unmarshal(data, &rec); err != nil {
				t.Fatalf("decode descriptor: %v", err)
			}
			if rec["operation"] != "present" {
				t.Fatalf("operation = %v", rec["operation"])
			}
			if rec["record_type"] != tc.recordType {
				t.Fatalf("record_type = %v", rec["record_type"])
			}
			if rec["name"] != tc.name {
				t.Fatalf("name = %v", rec["name"])
			}
			if rec["value"] != tc.value {
				t.Fatalf("value = %v", rec["value"])
			}

			// Acknowledge by removing the descriptor.
			if err := os.Remove(outPath); err != nil {
				t.Fatalf("rm descriptor: %v", err)
			}

			select {
			case rep := <-ch:
				if rep.Err != nil {
					t.Fatalf("present: %v", rep.Err)
				}
				if rep.Result.ID == "" {
					t.Fatal("empty id")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("present did not return after rm")
			}
		})
	}
}

func TestCleanup_OperatorAck(t *testing.T) {
	opts, outPath := configuredOpts(t)
	p := spawnPlugin(t, opts)

	// First present a record so we have an id and an internal record.
	pCh := p.presentAsync(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge.example.com", Value: "x", TTL: 60,
	}, 10*time.Second)
	waitForFile(t, outPath, 5*time.Second)
	if err := os.Remove(outPath); err != nil {
		t.Fatalf("rm: %v", err)
	}
	rep := <-pCh
	if rep.Err != nil {
		t.Fatalf("present: %v", rep.Err)
	}
	id := rep.Result.ID

	// Now cleanup.
	cCh := p.cleanupAsync(t, id, 10*time.Second)
	waitForFile(t, outPath, 5*time.Second)
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read cleanup descriptor: %v", err)
	}
	var rec map[string]any
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rec["operation"] != "cleanup" {
		t.Fatalf("operation = %v", rec["operation"])
	}
	if rec["id"] != id {
		t.Fatalf("id = %v, want %s", rec["id"], id)
	}
	if err := os.Remove(outPath); err != nil {
		t.Fatalf("rm: %v", err)
	}
	select {
	case err := <-cCh:
		if err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not return")
	}
}

// TestList_EnumeratesPresented presents two records and asserts both
// come back via dns.list. The manual plugin tracks records in-memory.
func TestList_EnumeratesPresented(t *testing.T) {
	opts, outPath := configuredOpts(t)
	p := spawnPlugin(t, opts)

	present := func(value string) string {
		ch := p.presentAsync(t, sdk.DNSPresentParams{
			Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge.example.com", Value: value, TTL: 60,
		}, 10*time.Second)
		waitForFile(t, outPath, 5*time.Second)
		if err := os.Remove(outPath); err != nil {
			t.Fatalf("rm: %v", err)
		}
		rep := <-ch
		if rep.Err != nil {
			t.Fatalf("present: %v", rep.Err)
		}
		return rep.Result.ID
	}

	id1 := present("first")
	id2 := present("second")

	recs, err := p.list(t, sdk.DNSListParams{Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge.example.com"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	got := map[string]string{}
	for _, r := range recs {
		got[r.ID] = r.Value
	}
	if got[id1] != "first" || got[id2] != "second" {
		t.Fatalf("recs = %+v", recs)
	}
}

// TestPresent_UnknownRecordType asserts the plugin rejects unsupported
// types without writing the descriptor file.
func TestPresent_UnknownRecordType(t *testing.T) {
	opts, outPath := configuredOpts(t)
	p := spawnPlugin(t, opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var res sdk.DNSPresentResult
	err := p.client.Call(ctx, sdk.MethodDNSPresent, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "PTR", Name: "x.example.com", Value: "v", TTL: 60,
	}, &res)
	if err == nil {
		t.Fatal("expected error")
	}
	var rpcErr *plug.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err type = %T", err)
	}
	if !strings.Contains(rpcErr.Message, "PTR") && !strings.Contains(rpcErr.Message, "unsupported") {
		t.Fatalf("msg = %q", rpcErr.Message)
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descriptor file exists, want not-exists: %v", err)
	}
}

// TestPresent_TimeoutWithoutAck pins that dns.present returns an error
// when the operator never deletes the file before confirm_timeout
// expires. We use a very short timeout so the test stays fast.
func TestPresent_TimeoutWithoutAck(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "pending.json")
	opts := map[string]any{
		"output_path":             outPath,
		"confirm_timeout_seconds": 1, // shortest the schema accepts
		"poll_interval_seconds":   0.05,
	}
	p := spawnPlugin(t, opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var res sdk.DNSPresentResult
	err := p.client.Call(ctx, sdk.MethodDNSPresent, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge.example.com", Value: "x", TTL: 60,
	}, &res)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var rpcErr *plug.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err type = %T", err)
	}
	if !strings.Contains(rpcErr.Message, "timeout") && !strings.Contains(rpcErr.Message, "confirm") {
		t.Fatalf("msg = %q, want timeout indicator", rpcErr.Message)
	}
}
