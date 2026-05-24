package main

import (
	"bytes"
	"net/url"
	"text/template"
)

// cloudInitTmpl is the user-data injected into every newly-spawned VM.
// It registers a one-shot runner with the supplied token and labels,
// installs a systemd unit to keep the forgejo-runner daemon alive,
// and starts it. The runner stays attached until the controller reaps
// the VM (default: at VM age > vm_max_lifetime). Each VM picks up
// jobs sequentially over its lifetime; parallel jobs are served by
// additional VMs that the controller spawns on subsequent ticks.
//
// Why systemd instead of nohup / a screen session: systemd keeps the
// daemon's stdout in the journal (so `journalctl -u forgejo-runner`
// is the debugging entry point) and restarts on crash inside the
// VM's lifetime.
var cloudInitTmpl = template.Must(template.New("cloudinit").Parse(`#!/usr/bin/env bash
set -euxo pipefail

# Wait for the network. Forgejo registration needs HTTPS to
# {{.Host}}, and cloud-init can fire before resolved is ready.
for _ in $(seq 1 30); do
  if getent hosts {{.Host}} >/dev/null; then break; fi
  sleep 1
done

mkdir -p /etc/forgejo-runner /var/lib/forgejo-runner
chown -R root:root /etc/forgejo-runner /var/lib/forgejo-runner

cat >/etc/forgejo-runner/config.yaml <<'CFGEOF'
log:
  level: info
runner:
  file: /var/lib/forgejo-runner/.runner
  capacity: 1
  envs: {}
  fetch_timeout: 5s
  fetch_interval: 2s
container:
  network: ""
  privileged: false
  options: ""
host:
  workdir_parent: ""
CFGEOF

# Register exactly once. Forgejo's register subcommand writes
# /var/lib/forgejo-runner/.runner that the daemon then reads.
#
# The ":host" suffix on every label tells act_runner to run matching
# jobs directly on this VM's host OS instead of inside a default
# node:22-bookworm container. The bake snapshot already has docker,
# go, node, pnpm, python3.12 + pip, pre-commit, Playwright deps, and
# the other toolchain CI needs (see infra/hetzner/provision.sh), so
# host-mode is the cheapest way to unblock jobs that need docker
# (docker-build, jmap conformance) and to make localhost:5432-style
# service mappings work the way GitHub-hosted runners do.
/usr/local/bin/forgejo-runner register \
  --instance "{{.Instance}}" \
  --token "{{.RegistrationToken}}" \
  --no-interactive \
  --name "{{.Name}}" \
  --labels "self-hosted:host,herold:host,{{.Arch}}:host" \
  --config /etc/forgejo-runner/config.yaml

cat >/etc/systemd/system/forgejo-runner.service <<'SVCEOF'
[Unit]
Description=Forgejo Actions runner (herold-ci, ephemeral)
After=network-online.target docker.service
Wants=network-online.target docker.service

[Service]
Type=simple
WorkingDirectory=/var/lib/forgejo-runner
ExecStart=/usr/local/bin/forgejo-runner daemon --config /etc/forgejo-runner/config.yaml
Restart=on-failure
RestartSec=5
# Pick up the daemon's stdout cleanly in journalctl.
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
SVCEOF

systemctl daemon-reload
systemctl enable --now forgejo-runner.service
`))

type cloudInitParams struct {
	Name              string
	Arch              string
	Instance          string // Forgejo base URL the runner registers against
	Host              string // Hostname derived from Instance, used in the DNS-wait probe
	RegistrationToken string
}

func renderCloudInit(p cloudInitParams) ([]byte, error) {
	if p.Host == "" && p.Instance != "" {
		if u, err := url.Parse(p.Instance); err == nil && u.Host != "" {
			p.Host = u.Hostname()
		}
	}
	var buf bytes.Buffer
	if err := cloudInitTmpl.Execute(&buf, p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
