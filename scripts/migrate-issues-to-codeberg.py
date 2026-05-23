#!/usr/bin/env python3
"""Migrate open GitHub issues from hanshuebner/herold to codeberg.org/hanshuebner/herold.

Source:  `gh issue list` (uses your existing gh authentication)
Target:  Codeberg Forgejo API (token read from $CODEBERG_TOKEN)

Behaviour:
  - Skips GitHub issues whose title already exists on the Codeberg target.
    Re-run safely after a partial migration (e.g. after hitting a rate limit).
  - Paces itself to respect Codeberg's "5 new issues per 5 minutes per user"
    rate limit by sleeping between creates, and retries on HTTP 429.

Pre-conditions:
  - The Codeberg repo exists with issues enabled.
  - `gh` CLI is logged in to GitHub.

Usage:
  ./scripts/migrate-issues-to-codeberg.py --dry-run
  CODEBERG_TOKEN=... ./scripts/migrate-issues-to-codeberg.py
"""

import argparse
import datetime
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

# Codeberg's new-issue rate limit appears to be a layered sliding window:
# at least 5 per 5 minutes AND 7 per 10 minutes per user, observed 2026-05-23.
# 65 s between creates respects the inner window; the 429 retry handles the
# outer one with a 310 s backoff.
ISSUE_CREATE_INTERVAL_SEC = 65
RATE_LIMIT_BACKOFF_SEC = 310

GH_REPO = "hanshuebner/herold"
CB_REPO = "hanshuebner/herold"
CB_BASE = "https://codeberg.org/api/v1"


def gh_list_open_issues():
    out = subprocess.check_output(
        ["gh", "-R", GH_REPO, "issue", "list",
         "--state", "open", "--limit", "500",
         "--json", "number,title,body,labels,createdAt,author"],
        text=True,
    )
    issues = json.loads(out)
    # gh returns newest first; create oldest first so Codeberg's auto-numbering
    # roughly mirrors the source chronology.
    issues.sort(key=lambda i: i["number"])
    return issues


def cb_request(method, path, token, body=None):
    url = f"{CB_BASE}{path}"
    data = json.dumps(body).encode("utf-8") if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Authorization", f"token {token}")
    req.add_header("Accept", "application/json")
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req) as resp:
            text = resp.read().decode("utf-8")
            return resp.status, json.loads(text) if text else None
    except urllib.error.HTTPError as e:
        text = e.read().decode("utf-8", errors="replace")
        sys.stderr.write(f"\nHTTP {e.code} on {method} {url}\n{text}\n")
        raise


def cb_list_labels(token):
    _, labels = cb_request("GET", f"/repos/{CB_REPO}/labels?limit=50", token)
    return labels or []


def cb_create_label(token, name, color, description):
    if not color.startswith("#"):
        color = "#" + color
    _, label = cb_request(
        "POST", f"/repos/{CB_REPO}/labels", token,
        body={"name": name, "color": color, "description": description or ""},
    )
    return label


def cb_list_open_issues(token):
    issues = []
    page = 1
    while True:
        _, batch = cb_request(
            "GET",
            f"/repos/{CB_REPO}/issues?state=open&type=issues&limit=50&page={page}",
            token,
        )
        if not batch:
            break
        issues.extend(batch)
        if len(batch) < 50:
            break
        page += 1
    return issues


def cb_create_issue(token, title, body, label_ids):
    while True:
        try:
            _, issue = cb_request(
                "POST", f"/repos/{CB_REPO}/issues", token,
                body={"title": title, "body": body, "labels": label_ids},
            )
            return issue
        except urllib.error.HTTPError as e:
            if e.code == 429:
                print(
                    f"    rate-limited; sleeping {RATE_LIMIT_BACKOFF_SEC}s "
                    "before retry",
                    file=sys.stderr,
                )
                time.sleep(RATE_LIMIT_BACKOFF_SEC)
                continue
            raise


def build_body(gh_issue):
    today = datetime.date.today().isoformat()
    original = (gh_issue.get("body") or "").rstrip()
    footer = (
        f"\n\n---\n"
        f"Migrated from https://github.com/{GH_REPO}/issues/{gh_issue['number']} "
        f"on {today}."
    )
    return original + footer


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--dry-run", action="store_true",
                    help="print what would happen, no Codeberg mutations")
    args = ap.parse_args()

    token = os.environ.get("CODEBERG_TOKEN")
    if not token and not args.dry_run:
        sys.exit("CODEBERG_TOKEN env var required (or pass --dry-run)")

    print(f"Source: github.com/{GH_REPO}", file=sys.stderr)
    print(f"Target: codeberg.org/{CB_REPO}", file=sys.stderr)
    print(f"Mode:   {'DRY RUN' if args.dry_run else 'LIVE'}", file=sys.stderr)
    print(file=sys.stderr)

    issues = gh_list_open_issues()
    print(f"Found {len(issues)} open issues on GitHub", file=sys.stderr)
    if not issues:
        return

    needed = {}
    for issue in issues:
        for lbl in issue["labels"]:
            needed[lbl["name"]] = {
                "name": lbl["name"],
                "color": lbl["color"],
                "description": lbl.get("description") or "",
            }

    if args.dry_run:
        existing = {}
        existing_titles = set()
    else:
        existing = {lbl["name"]: lbl for lbl in cb_list_labels(token)}
        existing_titles = {i["title"] for i in cb_list_open_issues(token)}
        if existing_titles:
            print(
                f"Codeberg already has {len(existing_titles)} open issue(s); "
                "matching titles will be skipped.",
                file=sys.stderr,
            )

    label_id_by_name = {}
    for name, spec in sorted(needed.items()):
        if name in existing:
            label_id_by_name[name] = existing[name]["id"]
            print(f"  label OK      {name}", file=sys.stderr)
            continue
        print(f"  label CREATE  {name}  (#{spec['color']})", file=sys.stderr)
        if not args.dry_run:
            created = cb_create_label(
                token, spec["name"], spec["color"], spec["description"],
            )
            label_id_by_name[name] = created["id"]

    print(file=sys.stderr)

    created = 0
    for gh in issues:
        title = gh["title"]
        if title in existing_titles:
            print(f"  issue SKIP    gh#{gh['number']:>3}  (already on Codeberg)  {title[:60]}", file=sys.stderr)
            continue
        body = build_body(gh)
        label_ids = [
            label_id_by_name[lbl["name"]]
            for lbl in gh["labels"]
            if lbl["name"] in label_id_by_name
        ]
        label_names = ", ".join(lbl["name"] for lbl in gh["labels"]) or "-"
        print(
            f"  issue CREATE  gh#{gh['number']:>3}  "
            f"{label_names:<40.40}  {title[:60]}",
            file=sys.stderr,
        )
        if not args.dry_run:
            if created > 0:
                time.sleep(ISSUE_CREATE_INTERVAL_SEC)
            new = cb_create_issue(token, title, body, label_ids)
            print(f"                  -> codeberg#{new['number']}", file=sys.stderr)
            created += 1

    print(file=sys.stderr)
    print("Done.", file=sys.stderr)
    if not args.dry_run:
        print(f"Verify: https://codeberg.org/{CB_REPO}/issues", file=sys.stderr)


if __name__ == "__main__":
    main()
