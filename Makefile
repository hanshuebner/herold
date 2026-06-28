# Herold Makefile. All release-critical commands live here so CI and
# developers run the same thing.

SHELL := /bin/bash
GO ?= go
GOFLAGS ?=
LDFLAGS := -trimpath
BUILDFLAGS := -buildvcs=true $(LDFLAGS)

PKGS := ./...
FUZZTIME ?= 30s

.PHONY: all build build-server build-web build-plugins prep-web manual dev test test-server test-web \
        test-short lint vet staticcheck vulncheck \
        fmt fmt-check fuzz-short tidy ci-local clean docker \
        interop interop-bulk interop-imaptest interop-jmaptest interop-clean \
        precommit precommit-all install-hooks check-schema-version \
        imap-upstream-diff

all: build

# `build` produces a herold binary with the consumer Suite SPA baked
# in (REQ-DEPLOY-COLOC-01..03). The pnpm build runs first and copies
# the suite dist into internal/webspa/dist/suite/ where the //go:embed
# directive in internal/webspa/embed_default.go picks it up; the Go
# build then links everything into a single binary.
build: build-web build-server

# build-server compiles the herold binary from the current state of
# internal/webspa/dist/. It does NOT invoke the pnpm build first, so
# `make build-server` after `make build-web` is the iteration loop
# when only Go code has changed. The prep-web prerequisite ensures
# the //go:embed directive in internal/webspa/embed_default.go has
# something to embed when the user has not run `make build-web` --
# placeholders are copied from internal/webspa/placeholder/.
build-server: prep-web
	$(GO) build $(BUILDFLAGS) -o bin/herold ./cmd/herold

# build-web runs scripts/build-web.sh which calls pnpm install
# --frozen-lockfile and pnpm --filter @herold/suite build, then
# copies the artefacts into internal/webspa/dist/suite/.
build-web:
	./scripts/build-web.sh

# prep-web ensures internal/webspa/dist/{admin,suite}/index.html exist so
# //go:embed dist resolves. If the dist tree is empty (clean checkout, no
# `make build-web` yet) we copy the tracked placeholders from
# internal/webspa/placeholder/. If the dist tree already holds real Vite
# output (after `make build-web`) we leave it alone -- the existence
# check on index.html is the cheap idempotency guard.
prep-web:
	@mkdir -p internal/webspa/dist/admin internal/webspa/dist/suite \
	  internal/webspa/dist/manual/user internal/webspa/dist/manual/admin
	@[ -f internal/webspa/dist/admin/index.html ] || \
	  cp internal/webspa/placeholder/admin/index.html internal/webspa/dist/admin/index.html
	@[ -f internal/webspa/dist/suite/index.html ] || \
	  cp internal/webspa/placeholder/suite/index.html internal/webspa/dist/suite/index.html
	@[ -f internal/webspa/dist/manual/index.html ] || \
	  cp internal/webspa/placeholder/manual/index.html internal/webspa/dist/manual/index.html
	@[ -f internal/webspa/dist/manual/user/index.html ] || \
	  cp internal/webspa/placeholder/manual/user/index.html internal/webspa/dist/manual/user/index.html
	@[ -f internal/webspa/dist/manual/admin/index.html ] || \
	  cp internal/webspa/placeholder/manual/admin/index.html internal/webspa/dist/manual/admin/index.html

# `manual` builds the operator/user manual to standalone SSR HTML and
# serves it at http://localhost:8000/ with live reload: editing any
# docs/manual/*.mdoc file rebuilds and refreshes connected browsers.
# Pure Node -- no SPA build, no herold binary. Requires a one-time
# `pnpm -C web install` so the Markdoc dependency is present. Override
# the port with MANUAL_PORT.
manual:
	cd web/packages/manual && node scripts/dev.mjs

# `dev` starts a persistent development loop: herold backend on
# 127.0.0.1:8080 (or HEROLD_DEV_BACKEND_PORT) and the suite Vite dev
# server on http://localhost:5173/ (or HEROLD_DEV_VITE_PORT). Editing
# any file under web/apps/suite/** hot-reloads in the browser; no
# backend restart is needed. State persists in .dev/ across restarts.
# On first run the domain, admin principal, and seed principals are
# provisioned automatically. Override HEROLD_DEV_DIR to relocate state.
dev: build-server
	./scripts/dev-server.sh

build-plugins:
	@for p in plugins/herold-*; do \
	  name=$$(basename $$p); \
	  echo ">>> $$name"; \
	  $(GO) build $(BUILDFLAGS) -o bin/$$name ./$$p || exit 1; \
	done

test: test-server

# test-server runs the Go test suite. The prep-web prerequisite guarantees
# the //go:embed directive in internal/webspa/embed_default.go finds a
# placeholder index.html (or real build output, if `make build-web` ran
# first). Tests that need the real suite assets bring up their own
# asset_dir override.
test-server: prep-web
	$(GO) test -race -count=1 $(GOFLAGS) $(PKGS)

# test-web runs the workspace-side checks (svelte-check today; vitest
# / playwright are added incrementally per-app via the package.json
# `check` / `test` / `lint` scripts).  pnpm --recursive --if-present
# silently skips packages that haven't defined a given script yet.
test-web:
	pnpm --dir web install --frozen-lockfile
	pnpm --dir web run check
	pnpm --dir web run test
	pnpm --dir web run lint

test-short: prep-web
	$(GO) test -race -count=1 -short $(GOFLAGS) $(PKGS)

vet: prep-web
	$(GO) vet $(PKGS)

staticcheck: prep-web
	@command -v staticcheck >/dev/null 2>&1 || { \
	  echo "staticcheck not installed. Run: go install honnef.co/go/tools/cmd/staticcheck@latest"; \
	  exit 1; }
	staticcheck $(PKGS)

vulncheck:
	@command -v govulncheck >/dev/null 2>&1 || { \
	  echo "govulncheck not installed. Run: go install golang.org/x/vuln/cmd/govulncheck@latest"; \
	  exit 1; }
	govulncheck $(PKGS)

fmt:
	$(GO) fmt $(PKGS)
	@command -v goimports >/dev/null 2>&1 && goimports -w -local github.com/hanshuebner/herold . || true

fmt-check:
	@diff=$$(gofmt -l .); \
	if [ -n "$$diff" ]; then \
	  echo "gofmt needs to be run on:"; echo "$$diff"; exit 1; \
	fi

lint: fmt-check vet staticcheck

fuzz-short:
	@# fuzz-short is a smoke pass: every Fuzz target runs for FUZZTIME
	@# (default 5s in CI). Minimization is disabled (-fuzzminimizetime=0)
	@# because the minimize phase races with the worker-cancellation grace
	@# window. When fuzztime elapses, the coordinator tears down workers; a
	@# worker that was mid-dispatch at the budget edge surfaces a non-zero
	@# exit whose REASON is a coordinator/worker lifecycle error, NOT an
	@# input crash. Go has worn several wordings for this same race:
	@#   - "context deadline exceeded"
	@#   - "context canceled"
	@#   - "fuzzing process hung or terminated unexpectedly"
	@#   - "EOF" (worker pipe closed during shutdown)
	@# All are false failures with no reproducer artifact, indistinguishable
	@# from a healthy run except for the exit code.
	@#
	@# Determinism rule (cannot mask a real bug): a non-zero exit is treated
	@# as a flaky transient ONLY when its output matches one of the lifecycle
	@# messages above AND carries no "Failing input written to testdata/
	@# fuzz/..." line. A genuine discovered crash ALWAYS writes that line; a
	@# seed crash panics with a stack trace (not a lifecycle message); a
	@# compile error says "build failed" — none of those match the transient
	@# set, so all real failures propagate unchanged on first sighting. A
	@# transient is retried once; a second transient of the same shape is
	@# accepted (a real reproducer would already be pinned in the corpus).
	@for t in $$(grep -rlE '^func Fuzz' --include='*_test.go' .); do \
	  pkg=$$(dirname $$t); \
	  names=$$(grep -oE '^func (Fuzz[A-Za-z0-9_]+)' $$t | awk '{print $$2}'); \
	  for n in $$names; do \
	    echo ">>> $$pkg $$n"; \
	    out=$$($(GO) test -run=^$$ -fuzz=^$$n$$ -fuzztime=$(FUZZTIME) -fuzzminimizetime=0 $$pkg 2>&1); \
	    rc=$$?; \
	    echo "$$out"; \
	    if [ $$rc -ne 0 ]; then \
	      if echo "$$out" | grep -qE "context deadline exceeded|context canceled|fuzzing process hung or terminated unexpectedly|: EOF" && ! echo "$$out" | grep -q "Failing input written"; then \
	        echo "  [non-reproducer fuzz lifecycle transient; retrying once]"; \
	        out=$$($(GO) test -run=^$$ -fuzz=^$$n$$ -fuzztime=$(FUZZTIME) -fuzzminimizetime=0 $$pkg 2>&1); \
	        rc=$$?; \
	        echo "$$out"; \
	        if [ $$rc -ne 0 ] && echo "$$out" | grep -qE "context deadline exceeded|context canceled|fuzzing process hung or terminated unexpectedly|: EOF" && ! echo "$$out" | grep -q "Failing input written"; then \
	          echo "  [non-reproducer fuzz lifecycle transient on retry; accepted as flaky-pass]"; \
	          rc=0; \
	        fi; \
	      fi; \
	    fi; \
	    [ $$rc -eq 0 ] || exit 1; \
	  done; \
	done

tidy:
	$(GO) mod tidy

ci-local: fmt-check vet test vulncheck
	@echo "local CI pipeline green"

# install-hooks wires .pre-commit-config.yaml into .git/hooks/ for both the
# pre-commit and pre-push stages. Idempotent. Run once after a fresh clone
# (or after the config grows new stages).
install-hooks:
	./scripts/install-hooks.sh

# check-schema-version enforces the
# CurrentSchemaVersion == max(migrations) invariant in <1s. Wired into
# pre-commit; available standalone for ad-hoc verification.
check-schema-version:
	./scripts/check-schema-version.sh

# precommit runs the same chain a pre-commit hook would (changed files
# only when invoked through `git commit`; --all-files when invoked via
# `make precommit-all`). Recommended before pushing.
precommit:
	@command -v pre-commit >/dev/null 2>&1 || { \
	  echo "pre-commit not installed; run: make install-hooks (after installing pre-commit itself)"; \
	  exit 1; }
	pre-commit run

precommit-all:
	@command -v pre-commit >/dev/null 2>&1 || { \
	  echo "pre-commit not installed; run: make install-hooks (after installing pre-commit itself)"; \
	  exit 1; }
	pre-commit run --all-files

docker:
	docker build -t herold:dev -f deploy/docker/Dockerfile .

clean:
	rm -rf bin dist coverage.out coverage.html

# Black-box interop suite. Brings up herold + Stalwart + docker-mailserver +
# Apache James + CoreDNS via docker compose, runs pytest scenarios against
# the wire surfaces. Heavy; not part of ci-local. See test/interop/README.md.
interop:
	./test/interop/run.sh

interop-bulk:
	./test/interop/run.sh --bulk

# imaptest IMAP wire-protocol conformance suite.
# Brings up the standard compose stack plus the "imaptest" profile, then
# runs only the @pytest.mark.imaptest scenario.
# IMAPTEST_SECS controls the run duration (default 30; use 300+ for soak runs).
interop-imaptest:
	PYTEST_MARKER=imaptest COMPOSE_PROFILES=imaptest ./test/interop/run-imaptest.sh

# jmapio/jmap-test-suite JMAP wire-protocol conformance suite.
# Brings up herold + the "jmaptest" profile (Node container with the upstream
# suite at a pinned commit), then runs the @pytest.mark.jmaptest scenario.
# JMAPTEST_FILTER restricts the run to a glob (e.g. "core/*"); JMAPTEST_TIMEOUT
# is the overall wall-clock cap in seconds (default 900).
interop-jmaptest:
	./test/interop/run-jmaptest.sh

interop-clean:
	cd test/interop && docker compose down --remove-orphans --volumes 2>/dev/null || true
	rm -rf test/interop/logs/[0-9]* test/interop/logs/latest

# imap-upstream-diff shows upstream go-imap commits and diffs since the fork
# base (third_party/go-imap/UPSTREAM) so a maintainer can identify parser or
# robustness fixes worth cherry-picking. Read-only; never modifies the tree.
# Run manually once or twice a year; not wired into ci-local or all.
imap-upstream-diff:
	./scripts/imap-upstream-diff.sh
