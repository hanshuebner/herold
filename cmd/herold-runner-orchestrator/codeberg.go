package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// codebergClient is a thin wrapper around the Forgejo Actions REST API
// exposed at codeberg.org/api/v1/.... Three calls do the work the
// orchestrator needs:
//   - listJobsWithLabels : find queued jobs matching a pool's labels
//   - listRunners        : see who is already registered + busy
//   - registrationToken  : one-shot token for a new VM's cloud-init
//
// Paths and verbs match Codeberg's published swagger
// (https://codeberg.org/swagger.v1.json).
type codebergClient struct {
	base   string // e.g. "https://codeberg.org"
	token  string
	repo   string // "owner/name"
	http   *http.Client
	logger *slog.Logger
}

func (c *codebergClient) do(ctx context.Context, method, path string, query url.Values, into any) error {
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
		return fmt.Errorf("%s %s -> HTTP %d: %s", method, u, resp.StatusCode, string(body))
	}
	if into == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(into)
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

type listJobsResponse struct {
	Jobs []job `json:"jobs"`
}

// listJobsWithLabels returns jobs whose runs-on label set matches the
// requested labels (server-side filter on /actions/runners/jobs).
// Forgejo does not expose a status filter on this endpoint, so we
// trim to "queued" / "waiting" client-side. A returned job is one
// our pool should be ready to claim.
func (c *codebergClient) listJobsWithLabels(ctx context.Context, labels []string) ([]job, error) {
	q := url.Values{}
	if len(labels) > 0 {
		q.Set("labels", strings.Join(labels, ","))
	}
	var resp listJobsResponse
	err := c.do(ctx, "GET",
		"/repos/"+c.repo+"/actions/runners/jobs",
		q, &resp)
	if err != nil {
		return nil, err
	}
	out := resp.Jobs[:0]
	for _, j := range resp.Jobs {
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

type listRunnersResponse struct {
	Runners []runner `json:"runners"`
}

// listRunners returns runners currently registered to the repo.
// Used by the reaper to drop ghost registrations whose VM no longer
// exists.
func (c *codebergClient) listRunners(ctx context.Context) ([]runner, error) {
	var resp listRunnersResponse
	err := c.do(ctx, "GET",
		"/repos/"+c.repo+"/actions/runners",
		nil, &resp)
	if err != nil {
		return nil, err
	}
	return resp.Runners, nil
}

// registrationToken returns a fresh one-shot token a new VM can use
// with `forgejo-runner register` to attach itself to this repo.
// Forgejo exposes this as GET (no body) at
// /repos/.../actions/runners/registration-token.
func (c *codebergClient) registrationToken(ctx context.Context) (string, error) {
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

// deleteRunner removes a runner record from Codeberg. Used when we
// scrap a VM that registered but no longer reports online.
func (c *codebergClient) deleteRunner(ctx context.Context, runnerID int64) error {
	return c.do(ctx, "DELETE",
		fmt.Sprintf("/repos/%s/actions/runners/%d", c.repo, runnerID),
		nil, nil)
}
