# Herold Makefile. All release-critical commands live here so CI and
# developers run the same thing.

SHELL := /bin/bash
GO ?= go
GOFLAGS ?=
LDFLAGS := -trimpath
BUILDFLAGS := -buildvcs=true $(LDFLAGS)

PKGS := ./...
FUZZTIME ?= 30s

.PHONY: all build build-server build-web build-plugins prep-web test test-server test-web \
        test-short lint vet staticcheck vulncheck \
        fmt fmt-check fuzz-short tidy ci-local clean docker \
        interop interop-bulk interop-imaptest interop-clean

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
	@mkdir -p internal/webspa/dist/admin internal/webspa/dist/suite
	@[ -f internal/webspa/dist/admin/index.html ] || \
	  cp internal/webspa/placeholder/admin/index.html internal/webspa/dist/admin/index.html
	@[ -f internal/webspa/dist/suite/index.html ] || \
	  cp internal/webspa/placeholder/suite/index.html internal/webspa/dist/suite/index.html

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
	@for t in $$(grep -rlE '^func Fuzz' --include='*_test.go' .); do \
	  pkg=$$(dirname $$t); \
	  names=$$(grep -oE '^func (Fuzz[A-Za-z0-9_]+)' $$t | awk '{print $$2}'); \
	  for n in $$names; do \
	    echo ">>> $$pkg $$n"; \
	    $(GO) test -run=^$$ -fuzz=^$$n$$ -fuzztime=$(FUZZTIME) $$pkg || exit 1; \
	  done; \
	done

tidy:
	$(GO) mod tidy

ci-local: fmt-check vet test vulncheck
	@echo "local CI pipeline green"

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

interop-clean:
	cd test/interop && docker compose down --remove-orphans --volumes 2>/dev/null || true
	rm -rf test/interop/logs/[0-9]* test/interop/logs/latest
