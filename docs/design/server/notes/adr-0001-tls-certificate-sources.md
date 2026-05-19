# ADR-0001: TLS certificate sources and the Kubernetes posture

- Status: Accepted
- Date: 2026-05-19
- Area: server -- TLS, deployment
- Related requirements: REQ-OPS-40 (cert sources), REQ-OPS-41 / REQ-PROTO-72
  (SNI certificate store), REQ-OPS-30 (SIGHUP reload)

## Context

herold terminates TLS on many listeners: SMTP relay and submission, IMAP,
ManageSieve, and the two HTTP surfaces (the public Suite listener and the
admin listener). Two questions have been open.

### 1. First-boot certificates

The Docker image bakes a throwaway self-signed certificate into the image
layer, and the quickstart `system.toml` points its TLS listeners at it, so
the container boots end-to-end with no mounts. For a real deployment this
certificate is at best unused and at worst harmful: presenting a self-signed
certificate on a *mail* server's listeners trains users and administrators
to click through certificate warnings -- exactly the habit a mail server
must not cultivate. A real deployment should never bootstrap with a
self-signed certificate.

The objection sometimes raised against an in-process ACME client -- "the
certificate has to exist before the server starts" -- does not actually
hold. ACME's HTTP-01 challenge runs over plain HTTP on port 80 and needs no
certificate at all. A server can bind port 80, answer the challenge, obtain
the certificate, and only then bring its TLS listeners into service. The
self-signed certificate is a crutch, not a requirement.

### 2. Kubernetes

Kubernetes is a future target; standalone and Docker are the current
priority. A common assumption is that under Kubernetes TLS is terminated
outside the application container, at an Ingress or a load balancer.

That is true for HTTP, and it applies cleanly to herold's two SPAs. It is
**not** true for mail. SMTP and IMAP STARTTLS are in-band protocol upgrades
that an L4 load balancer cannot terminate -- the negotiation happens inside
the application protocol. Terminating implicit TLS (465 / 993) at an L4 load
balancer is possible but leaves the pod seeing a plaintext connection, which
breaks TLS-gated `AUTH` and loses the client's view of connection security.
Protocol-aware mail proxies exist but are not part of the standard k8s
toolkit.

The idiomatic Kubernetes pattern for mail is therefore L4 **passthrough**: a
`Service` of type `LoadBalancer` forwards raw TCP to the pod, and the pod
terminates mail TLS itself. Kubernetes does not remove in-pod certificate
handling. What it removes is *ACME* -- cert-manager is the idiomatic k8s
certificate issuer, and herold should consume its output rather than run a
second ACME client inside the cluster.

## Decision

### 1. Two certificate sources, selected per listener

Every TLS-terminating listener takes its certificate from one of two
sources:

- `acme` -- herold's built-in ACME client obtains and renews the
  certificate (HTTP-01, TLS-ALPN-01, or DNS-01 via a DNS-provider plugin).
- `file` -- herold loads an operator-supplied `cert_file` / `key_file`.

This is already partially modelled (`AdminTLSConfig.Source`, the per-listener
`tls` settings). This ADR makes the model explicit and uniform across every
listener, mail and HTTP alike.

### 2. No self-signed bootstrap

A listener whose source is `acme` and which has no certificate yet MUST:

- refuse the TLS handshake, for handshake-first listeners (HTTPS, implicit
  TLS on 465 / 993); and
- not advertise `STARTTLS`, for STARTTLS listeners (SMTP, IMAP,
  ManageSieve);

until a real certificate for the relevant hostname is present in the SNI
certificate store. It MUST NOT present a self-signed certificate to a real
client. The first-boot window is a few seconds, and a brand-new server has
no clients; refusing is strictly better than teaching click-through.

The baked-in self-signed certificate is removed from the Docker image. The
loopback quickstart `system.toml` sets `tls = "none"` on its mail listeners
-- plaintext on `127.0.0.1` is honest for a throwaway local evaluation, and
the HTTP listeners are already `tls = "none"`.

TLS-ALPN-01's challenge certificate is unaffected by this decision: it is
ephemeral, presented only to the ACME validation server during the
`acme-tls/1` handshake, and never seen by a real client.

### 3. The `file` source watches its files and hot-reloads

The `file` source watches `cert_file` / `key_file` for changes and reloads
the certificate into the SNI store without a restart and without requiring
an explicit `herold server reload`. One implementation then serves three
deployment shapes:

- standalone / Docker with operator-managed certificates (renewed by the
  operator's own tooling);
- a reverse proxy fronting the web SPAs while mail TLS is passed through to
  herold;
- Kubernetes, where cert-manager rotates a `Secret` and the projected
  volume mount updates underneath the pod.

Today certificate reload happens only on `SIGHUP` (`herold server reload`).
Native file-watching is the one piece of Kubernetes groundwork worth doing
ahead of first-class k8s support -- and it is independently justified by the
standalone bring-your-own-certificate case, so it is not k8s-specific work.

### 4. Kubernetes posture

When Kubernetes becomes a first-class target it is served by the model
above, with **no Kubernetes-specific code**:

- Mail ports are exposed by a `Service` of type `LoadBalancer` (or
  `NodePort`) with **L4 TLS passthrough**; the herold pod terminates mail
  TLS.
- cert-manager issues a `Certificate` into a `Secret`; the `Secret` is
  mounted into the pod as files; every herold listener uses the `file`
  source; the built-in ACME client is disabled.
- The web SPAs are either passed through to herold the same way, or fronted
  by an `Ingress` that terminates HTTPS with its own cert-manager
  certificate.
- A Helm chart packaging the `StatefulSet`, the `Service`s, the
  cert-manager `Certificate`, and the `PersistentVolumeClaim` for `data_dir`
  is a future deliverable.

Client-IP preservation across an L4 load balancer needs PROXY protocol on
the mail listeners (HTTP listeners already decode it). That is a separate,
optional follow-up, not a blocker for the cert model.

### 5. Scope and sequencing

Standalone and Docker are the current priority; Kubernetes is future. The
decision is sequenced so that the work needed now -- a `file` certificate
source that watches and hot-reloads -- is exactly the work Kubernetes
reuses later. No effort is spent on k8s-specific machinery before k8s is
prioritised; the manual carries only a short positioning stub until then.

## Consequences

Positive:

- No deployment, real or quickstart, bootstraps with a self-signed
  certificate. Mail clients are never shown a certificate they must click
  through.
- The Docker image carries no embedded keypair.
- One `file`-source implementation covers BYO certificates, reverse-proxy
  passthrough, and Kubernetes / cert-manager.
- Kubernetes support, when prioritised, is manifests and a Helm chart --
  no new server code.

Negative / costs:

- A brief first-boot window where `acme`-source TLS listeners refuse
  connections until the first certificate is issued. Acceptable: a new
  server has no clients.
- The quickstart mail listeners run plaintext on loopback. Slightly less
  realistic than STARTTLS, but honest and loopback-scoped.

Neutral:

- The `acme` source remains the default and recommended path for
  internet-facing standalone and Docker deployments. This ADR does not
  deprecate it; it removes the self-signed fallback beneath it.

## Implementation status

Decided here; tracked as GitHub issues.

1. Withhold `STARTTLS` advertisement until a certificate is available --
   issue #108. (Handshake-first listeners already refuse cleanly:
   `tls.Store.Get` returns `ErrNoCertificate`.)
2. Remove the baked-in self-signed certificate from the Docker image and
   run the quickstart container plaintext on loopback -- issue #109
   (covers `deploy/docker/Dockerfile` and `deploy/docker/system.toml`).
3. fsnotify-based file watching in the `file` certificate source, with
   hot-reload into the SNI store -- issue #110.
4. PROXY protocol support on the mail listeners -- issue #111. Optional,
   k8s-motivated; deferred behind 1-3.
