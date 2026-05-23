// herold-runner-orchestrator keeps ephemeral Forgejo Actions runners
// attached to a Codeberg repo just in time. On every tick it polls
// Codeberg for queued workflow jobs, polls Hetzner for currently
// spawned VMs, and reconciles the gap: spawning new VMs from the
// labelled snapshots and reaping VMs that are off or past their
// max lifetime.
//
// Designed to run on a small always-on host (the maintainer's FreeBSD
// VM in the design doc). Stateless: a restart picks up where it left
// off by re-deriving truth from the two APIs.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

type config struct {
	hetznerToken    string
	codebergToken   string
	codebergRepo    string
	codebergBaseURL string
	sshKeyName      string
	location        string
	armServerType   string
	amdServerType   string
	maxPerPoolArm   int
	maxPerPoolAmd   int
	pollInterval    time.Duration
	vmMaxLifetime   time.Duration
	logFormat       string
}

func loadConfig() (config, error) {
	c := config{
		codebergBaseURL: "https://codeberg.org",
		armServerType:   "cax21",
		amdServerType:   "cpx22",
		location:        "nbg1",
		maxPerPoolArm:   4,
		maxPerPoolAmd:   2,
		pollInterval:    15 * time.Second,
		vmMaxLifetime:   60 * time.Minute,
		logFormat:       "text",
	}

	flag.StringVar(&c.codebergRepo, "repo", envOr("ORCHESTRATOR_CODEBERG_REPO", "hanshuebner/herold"),
		"target Codeberg repo (owner/name)")
	flag.StringVar(&c.codebergBaseURL, "codeberg", envOr("ORCHESTRATOR_CODEBERG_URL", c.codebergBaseURL),
		"Codeberg base URL")
	flag.StringVar(&c.sshKeyName, "ssh-key", envOr("ORCHESTRATOR_SSH_KEY_NAME", ""),
		"hcloud SSH key name to inject (empty = first available)")
	flag.StringVar(&c.location, "location", envOr("ORCHESTRATOR_LOCATION", c.location),
		"Hetzner location for spawned VMs")
	flag.StringVar(&c.armServerType, "arm-type", envOr("ORCHESTRATOR_VM_TYPE_ARM64", c.armServerType),
		"Hetzner server type for arm64 runners")
	flag.StringVar(&c.amdServerType, "amd-type", envOr("ORCHESTRATOR_VM_TYPE_AMD64", c.amdServerType),
		"Hetzner server type for amd64 runners")
	flag.IntVar(&c.maxPerPoolArm, "max-arm", envInt("ORCHESTRATOR_MAX_PER_POOL_ARM64", c.maxPerPoolArm),
		"max concurrent arm64 runner VMs")
	flag.IntVar(&c.maxPerPoolAmd, "max-amd", envInt("ORCHESTRATOR_MAX_PER_POOL_AMD64", c.maxPerPoolAmd),
		"max concurrent amd64 runner VMs")
	flag.DurationVar(&c.pollInterval, "poll", envDuration("ORCHESTRATOR_POLL_INTERVAL", c.pollInterval),
		"reconciliation poll interval")
	flag.DurationVar(&c.vmMaxLifetime, "vm-max-lifetime", envDuration("ORCHESTRATOR_VM_MAX_LIFETIME", c.vmMaxLifetime),
		"reap VMs older than this even if still busy")
	flag.StringVar(&c.logFormat, "log", envOr("ORCHESTRATOR_LOG_FORMAT", c.logFormat),
		"log format: text or json")
	flag.Parse()

	c.hetznerToken = os.Getenv("HCLOUD_TOKEN")
	if c.hetznerToken == "" {
		c.hetznerToken = os.Getenv("ORCHESTRATOR_HETZNER_TOKEN")
	}
	c.codebergToken = os.Getenv("ORCHESTRATOR_CODEBERG_TOKEN")

	if c.hetznerToken == "" {
		return c, fmt.Errorf("HCLOUD_TOKEN or ORCHESTRATOR_HETZNER_TOKEN env var required")
	}
	if c.codebergToken == "" {
		return c, fmt.Errorf("ORCHESTRATOR_CODEBERG_TOKEN env var required")
	}
	if !strings.Contains(c.codebergRepo, "/") {
		return c, fmt.Errorf("--repo must be owner/name (got %q)", c.codebergRepo)
	}
	return c, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func makeLogger(format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(2)
	}
	log := makeLogger(cfg.logFormat)
	log.Info("orchestrator starting",
		"repo", cfg.codebergRepo,
		"location", cfg.location,
		"arm_type", cfg.armServerType,
		"amd_type", cfg.amdServerType,
		"max_arm", cfg.maxPerPoolArm,
		"max_amd", cfg.maxPerPoolAmd,
		"poll", cfg.pollInterval,
		"vm_max_lifetime", cfg.vmMaxLifetime,
	)

	hc := hcloud.NewClient(hcloud.WithToken(cfg.hetznerToken))
	cb := &codebergClient{
		base:   cfg.codebergBaseURL,
		token:  cfg.codebergToken,
		repo:   cfg.codebergRepo,
		http:   &http.Client{Timeout: 20 * time.Second},
		logger: log,
	}

	orch := &orchestrator{
		cfg: cfg,
		hc:  hc,
		cb:  cb,
		log: log,
	}

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := orch.run(ctx); err != nil && ctx.Err() == nil {
		log.Error("orchestrator exited with error", "err", err)
		os.Exit(1)
	}
	log.Info("orchestrator stopped")
}
