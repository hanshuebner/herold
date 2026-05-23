package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
)

// codebergClient is a thin wrapper around the Forgejo Actions REST API
// exposed at codeberg.org/api/v1/.... We only need a handful of calls:
//   - queued workflow runs (so we know what to scale up for)
//   - jobs of a run (to read the runs-on labels)
//   - currently-registered runners (so we know who is already in the pool)
//   - one-shot registration token (handed to a new VM via cloud-init)
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

// workflowRun is the trimmed shape of an Actions workflow run entry.
// Forgejo follows the GitHub-compatible schema closely enough that
// only the few fields we read are pinned here; unknown fields are
// ignored by encoding/json.
type workflowRun struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HeadBranch string `json:"head_branch"`
	WorkflowID int64  `json:"workflow_id"`
	Name       string `json:"name"`
}

type listRunsResponse struct {
	TotalCount   int           `json:"total_count"`
	WorkflowRuns []workflowRun `json:"workflow_runs"`
}

// listQueuedRuns returns workflow runs reported as not-yet-completed
// (queued + waiting). The API status filter is exposed by Forgejo's
// /actions/runs endpoint; we ask for "queued" explicitly. A second
// call for "waiting" catches runs that are blocked on env approval —
// not currently used by herold's workflows but cheap to cover.
func (c *codebergClient) listQueuedRuns(ctx context.Context) ([]workflowRun, error) {
	var out []workflowRun
	for _, status := range []string{"queued", "waiting", "in_progress"} {
		q := url.Values{"status": []string{status}, "per_page": []string{"50"}}
		var resp listRunsResponse
		err := c.do(ctx, "GET", "/repos/"+c.repo+"/actions/runs", q, &resp)
		if err != nil {
			return nil, fmt.Errorf("listRuns(status=%s): %w", status, err)
		}
		out = append(out, resp.WorkflowRuns...)
	}
	return out, nil
}

type job struct {
	ID         int64    `json:"id"`
	RunID      int64    `json:"run_id"`
	Status     string   `json:"status"`     // "queued" | "in_progress" | "completed"
	Conclusion string   `json:"conclusion"` // when completed
	Labels     []string `json:"labels"`
	RunnerName string   `json:"runner_name"`
}

type listJobsResponse struct {
	TotalCount int   `json:"total_count"`
	Jobs       []job `json:"jobs"`
}

// listJobs returns the per-job entries for a run. Each entry carries
// the runs-on label list which is what we use to route a queued job
// to the right arch pool.
func (c *codebergClient) listJobs(ctx context.Context, runID int64) ([]job, error) {
	var resp listJobsResponse
	err := c.do(ctx, "GET",
		fmt.Sprintf("/repos/%s/actions/runs/%d/jobs", c.repo, runID),
		nil, &resp)
	if err != nil {
		return nil, fmt.Errorf("listJobs(run=%d): %w", runID, err)
	}
	return resp.Jobs, nil
}

type runner struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Status string   `json:"status"` // "online" | "offline"
	Busy   bool     `json:"busy"`
	Labels []string `json:"labels"`
}

type listRunnersResponse struct {
	TotalCount int      `json:"total_count"`
	Runners    []runner `json:"runners"`
}

// listRunners returns the runners currently registered to the repo.
// We don't differentiate between fresh and stale at this layer; the
// reconciler treats only runners that arch-match our pool labels as
// candidates.
func (c *codebergClient) listRunners(ctx context.Context) ([]runner, error) {
	var resp listRunnersResponse
	err := c.do(ctx, "GET",
		"/repos/"+c.repo+"/actions/runners",
		url.Values{"per_page": []string{"50"}}, &resp)
	if err != nil {
		return nil, err
	}
	return resp.Runners, nil
}

// registrationToken returns a fresh one-shot token a new VM can use
// with `forgejo-runner register` to attach itself to this repo.
// Tokens expire (typically ~1 hour for the issuer's window); we
// generate a fresh one per spawn.
func (c *codebergClient) registrationToken(ctx context.Context) (string, error) {
	var resp struct {
		Token string `json:"token"`
	}
	err := c.do(ctx, "POST",
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
