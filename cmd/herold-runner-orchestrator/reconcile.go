package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

type orchestrator struct {
	cfg config
	hc  *hcloud.Client
	cb  *codebergClient
	log interface {
		Info(msg string, args ...any)
		Warn(msg string, args ...any)
		Error(msg string, args ...any)
		Debug(msg string, args ...any)
	}
}

func (o *orchestrator) run(ctx context.Context) error {
	t := time.NewTicker(o.cfg.pollInterval)
	defer t.Stop()
	// Reconcile once immediately on start so we don't wait pollInterval
	// before doing anything useful.
	o.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			o.tick(ctx)
		}
	}
}

// tick is the single reconciliation pass. It is intentionally one
// flat function so the failure modes (Codeberg unreachable, Hetzner
// unreachable, no snapshot for arch) each log + skip the affected
// slice without aborting the rest of the pass.
func (o *orchestrator) tick(ctx context.Context) {
	tickCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	queuedPerArch, err := o.queuedJobsByArch(tickCtx)
	if err != nil {
		o.log.Warn("queue probe failed; skipping spawn this tick", "err", err)
		// Carry on to the reaper -- it doesn't need Codeberg.
		queuedPerArch = nil
	}

	vms, err := existingRunnerVMs(tickCtx, o.hc)
	if err != nil {
		o.log.Error("hetzner list failed; skipping tick", "err", err)
		return
	}
	vmsByArch := map[string][]*hcloud.Server{}
	for _, v := range vms {
		arch := v.Labels["arch"]
		vmsByArch[arch] = append(vmsByArch[arch], v)
	}

	// Reaper: any VM past its lifetime or already off is recycled.
	liveVMNames := map[string]bool{}
	now := time.Now()
	for _, v := range vms {
		age := now.Sub(v.Created)
		reap := false
		reason := ""
		switch {
		case v.Status == hcloud.ServerStatusOff:
			reap, reason = true, "powered off"
		case age > o.cfg.vmMaxLifetime:
			reap, reason = true, fmt.Sprintf("age %s > max %s", age.Truncate(time.Second), o.cfg.vmMaxLifetime)
		}
		if reap {
			o.log.Info("reaping VM", "id", v.ID, "name", v.Name, "arch", v.Labels["arch"], "reason", reason)
			if err := deleteVM(tickCtx, o.hc, v); err != nil {
				o.log.Warn("reap delete failed", "id", v.ID, "err", err)
			}
			continue
		}
		liveVMNames[v.Name] = true
	}

	// Ghost-runner cleanup: a runner that registered against the repo
	// but has no matching VM in Hetzner (the VM was reaped before the
	// runner cleanly deregistered) becomes dead weight in the Codeberg
	// UI and counts toward "online" in some scheduling code paths.
	// Match by name: cloud-init's --name flag matches the VM name we
	// pass to hcloud.
	runners, err := o.cb.listRunners(tickCtx)
	if err != nil {
		o.log.Warn("runner list failed; skipping ghost cleanup", "err", err)
	} else {
		for _, r := range runners {
			if !strings.HasPrefix(r.Name, "herold-runner-") {
				continue // only touch runners we own
			}
			if liveVMNames[r.Name] {
				continue
			}
			o.log.Info("removing ghost runner registration", "id", r.ID, "name", r.Name)
			if err := o.cb.deleteRunner(tickCtx, r.ID); err != nil {
				o.log.Warn("ghost runner delete failed", "id", r.ID, "err", err)
			}
		}
	}

	// Spawner: for each arch where we have queued work and capacity,
	// spawn enough VMs to cover the deficit.
	for _, arch := range []string{"arm64", "amd64"} {
		queued := queuedPerArch[arch]
		live := len(vmsByArch[arch])
		max := o.poolMax(arch)
		need := queued - live
		if need <= 0 {
			if queued > 0 {
				o.log.Debug("queue covered", "arch", arch, "queued", queued, "live", live)
			}
			continue
		}
		room := max - live
		spawn := need
		if spawn > room {
			spawn = room
		}
		if spawn <= 0 {
			o.log.Warn("pool at max; queue waits", "arch", arch, "queued", queued, "live", live, "max", max)
			continue
		}
		o.log.Info("scaling up", "arch", arch, "queued", queued, "live", live, "max", max, "spawn", spawn)
		for i := 0; i < spawn; i++ {
			if err := o.spawnOne(tickCtx, arch); err != nil {
				o.log.Error("spawn failed", "arch", arch, "err", err)
				break // one failure usually means the next will too
			}
		}
	}
}

// queuedJobsByArch returns a count of arch-distinguishable jobs the
// repo currently has waiting for a runner. We treat a job as
// "consuming a runner" if it is queued OR in_progress without a
// matching online runner. The naive in_progress count would let us
// over-spawn, but in practice Codeberg only marks a job in_progress
// after a runner has claimed it, so this approximation is fine for v1.
func (o *orchestrator) queuedJobsByArch(ctx context.Context) (map[string]int, error) {
	runs, err := o.cb.listQueuedRuns(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range runs {
		jobs, err := o.cb.listJobs(ctx, r.ID)
		if err != nil {
			o.log.Warn("listJobs failed; skipping run", "run", r.ID, "err", err)
			continue
		}
		for _, j := range jobs {
			if j.Status != "queued" {
				continue
			}
			arch := pickArchFromLabels(j.Labels)
			if arch == "" {
				o.log.Debug("queued job with no recognised arch label",
					"job", j.ID, "labels", strings.Join(j.Labels, ","))
				continue
			}
			out[arch]++
		}
	}
	return out, nil
}

func pickArchFromLabels(labels []string) string {
	hasSelfHosted := false
	hasHerold := false
	arch := ""
	for _, l := range labels {
		switch l {
		case "self-hosted":
			hasSelfHosted = true
		case "herold":
			hasHerold = true
		case "arm64":
			arch = "arm64"
		case "amd64":
			arch = "amd64"
		}
	}
	if !hasSelfHosted || !hasHerold {
		return ""
	}
	return arch
}

func (o *orchestrator) poolMax(arch string) int {
	switch arch {
	case "arm64":
		return o.cfg.maxPerPoolArm
	case "amd64":
		return o.cfg.maxPerPoolAmd
	default:
		return 0
	}
}

func (o *orchestrator) serverTypeFor(arch string) string {
	switch arch {
	case "arm64":
		return o.cfg.armServerType
	case "amd64":
		return o.cfg.amdServerType
	default:
		return ""
	}
}

func (o *orchestrator) spawnOne(ctx context.Context, arch string) error {
	snapshotID, err := latestSnapshotID(ctx, o.hc, arch)
	if err != nil {
		return fmt.Errorf("snapshot lookup: %w", err)
	}
	token, err := o.cb.registrationToken(ctx)
	if err != nil {
		return fmt.Errorf("registration token: %w", err)
	}
	name := fmt.Sprintf("herold-runner-%s-%s", arch,
		time.Now().UTC().Format("20060102-150405"))
	server, err := spawnVM(ctx, o.hc, spawnVMArgs{
		Name:              name,
		Arch:              arch,
		ServerType:        o.serverTypeFor(arch),
		Location:          o.cfg.location,
		SSHKeyName:        o.cfg.sshKeyName,
		SnapshotID:        snapshotID,
		RegistrationToken: token,
	})
	if err != nil {
		return err
	}
	o.log.Info("spawned VM",
		"id", server.ID,
		"name", server.Name,
		"arch", arch,
		"snapshot", snapshotID,
		"type", o.serverTypeFor(arch))
	return nil
}
