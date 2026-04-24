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
        fmt fmt-check fuzz-short tidy ci-local clean docker

all: build

build:
	$(GO) build $(BUILDFLAGS) -o bin/herold ./cmd/herold

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
