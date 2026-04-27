# Herold Makefile. All release-critical commands live here so CI and
# developers run the same thing.

SHELL := /bin/bash
GO ?= go
GOFLAGS ?=
LDFLAGS := -trimpath
BUILDFLAGS := -buildvcs=true $(LDFLAGS)

PKGS := ./...
FUZZTIME ?= 30s

.PHONY: all build build-plugins test test-short lint vet staticcheck vulncheck \
        fmt fmt-check fuzz-short tidy ci-local clean docker embed-tabard \
        interop interop-bulk interop-imaptest interop-clean

all: build

build:
	$(GO) build $(BUILDFLAGS) -o bin/herold ./cmd/herold

# embed-tabard copies the upstream tabard SPA dist into the herold
# module's internal/tabardspa/dist directory so the next `make build`
# bakes the SPA into the binary (REQ-DEPLOY-COLOC-01..03).
# Override the source path via TABARD_DIST=/path/to/tabard/dist.
embed-tabard:
	TABARD_DIST="$${TABARD_DIST:-/Users/hans/tabard/apps/suite/dist}" \
	  ./scripts/embed-tabard.sh

build-plugins:
	@for p in plugins/herold-*; do \
	  name=$$(basename $$p); \
	  echo ">>> $$name"; \
	  $(GO) build $(BUILDFLAGS) -o bin/$$name ./$$p || exit 1; \
	done

test:
	$(GO) test -race -count=1 $(GOFLAGS) $(PKGS)

test-short:
	$(GO) test -race -count=1 -short $(GOFLAGS) $(PKGS)

vet:
	$(GO) vet $(PKGS)

staticcheck:
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
