package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// forgejoClient is a thin wrapper around the Forgejo Actions REST API
// exposed by any Forgejo instance at /api/v1/.... Three calls do the
// work the orchestrator needs:
//   - listJobsWithLabels : find queued jobs matching a pool's labels
//   - listRunners        : see who is already registered + busy
//   - registrationToken  : one-shot token for a new VM's cloud-init
//
// Paths and verbs match the upstream Forgejo swagger
// (any-instance/swagger.v1.json), confirmed against our self-hosted
// instance at code.netzhansa.com.
type forgejoClient struct {
	base   string // e.g. "https://code.netzhansa.com"
	token  string
	repo   string // "owner/name"
	http   *http.Client
	logger *slog.Logger
}

// httpErr is the typed error returned by do() for any non-2xx
// response. Callers can errors.As against it to special-case status
// codes (e.g. 404 fall-through on optional endpoints).
type httpErr struct {
	method, url, body string
	code              int
}

func (e *httpErr) Error() string {
	return fmt.Sprintf("%s %s -> HTTP %d: %s", e.method, e.url, e.code, e.body)
}

func (c *forgejoClient) do(ctx context.Context, method, path string, query url.Values, into any) error {
	u := c.base + "/api/v1" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &httpErr{method: method, url: u, body: string(body), code: resp.StatusCode}
	}
	if into == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// isNotFound reports whether err is a 404 returned by do().
func isNotFound(err error) bool {
	var he *httpErr
	return errors.As(err, &he) && he.code == 404
}

// job mirrors Forgejo's ActionRunJob shape (the fields we actually
// read). RunsOn carries the runs-on label list which is what we
// already filtered by on the server side, so it's effectively echo
// here; we still keep it for log lines.
type job struct {
	ID     int64    `json:"id"`
	Status string   `json:"status"`
	Name   string   `json:"name"`
	RunsOn []string `json:"runs_on"`
}

// listJobsWithLabels returns jobs whose runs-on label set matches the
// requested labels (server-side filter on /actions/runners/jobs).
// Forgejo does not expose a status filter on this endpoint, so we
// trim to "queued" / "waiting" client-side. A returned job is one
// our pool should be ready to claim.
//
// Forgejo's response is a bare array of ActionRunJob, not a wrapped
// object - confirmed against code.netzhansa.com running Forgejo 14.0.5.
func (c *forgejoClient) listJobsWithLabels(ctx context.Context, labels []string) ([]job, error) {
	q := url.Values{}
	if len(labels) > 0 {
		q.Set("labels", strings.Join(labels, ","))
	}
	var jobs []job
	err := c.do(ctx, "GET",
		"/repos/"+c.repo+"/actions/runners/jobs",
		q, &jobs)
	if err != nil {
		return nil, err
	}
	out := jobs[:0]
	for _, j := range jobs {
		switch j.Status {
		case "queued", "waiting":
			out = append(out, j)
		}
	}
	return out, nil
}

// runner mirrors Forgejo's ActionRunner.
type runner struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Labels []string `json:"labels"`
}

// listRunners returns runners currently registered to the repo.
// Used by the reaper to drop ghost registrations whose VM no longer
// exists.
//
// Some Forgejo versions (14.0.5 confirmed on code.netzhansa.com) do
// not expose this route at the repo level - they return 404. Ghost
// cleanup is best-effort; an empty list + warning is the right
// degradation. Spawning works regardless because the registration-
// token endpoint is still reachable.
func (c *forgejoClient) listRunners(ctx context.Context) ([]runner, error) {
	var runners []runner
	err := c.do(ctx, "GET",
		"/repos/"+c.repo+"/actions/runners",
		nil, &runners)
	if err != nil {
		return nil, err
	}
	return runners, nil
}

// registrationToken returns a fresh one-shot token a new VM can use
// with `forgejo-runner register` to attach itself to this repo.
// Forgejo exposes this as GET (no body) at
// /repos/.../actions/runners/registration-token.
func (c *forgejoClient) registrationToken(ctx context.Context) (string, error) {
	var resp struct {
		Token string `json:"token"`
	}
	err := c.do(ctx, "GET",
		"/repos/"+c.repo+"/actions/runners/registration-token",
		nil, &resp)
	if err != nil {
		return "", err
	}
	if resp.Token == "" {
		return "", fmt.Errorf("empty registration token in response")
	}
	return resp.Token, nil
}

// deleteRunner removes a runner record from the Forgejo instance.
// Used when we scrap a VM that registered but no longer reports
// online.
func (c *forgejoClient) deleteRunner(ctx context.Context, runnerID int64) error {
	return c.do(ctx, "DELETE",
		fmt.Sprintf("/repos/%s/actions/runners/%d", c.repo, runnerID),
		nil, nil)
}
