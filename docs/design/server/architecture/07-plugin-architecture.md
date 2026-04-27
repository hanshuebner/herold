# 07 — Plugin architecture

How plugins are implemented. Requirements are in `requirements/11-plugins.md`; this doc is the *how*.

## One-line summary

Out-of-process child processes speaking newline-delimited JSON-RPC 2.0 on stdin/stdout. Server is the client; plugin is the server. Long-running plugins stay alive; short-lived run per invocation. Process boundary is the security boundary.

## Process model

```
                 herold (main process)
                 ┌──────────────────────┐
                 │   plugin manager     │
                 │  (supervises all)    │
                 └──────┬──────┬──────┬─┘
                        │      │      │
         pipe(stdin/stdout/stderr) per plugin
                        │      │      │
               ┌────────▼┐   ┌─▼────┐ ┌▼─────┐
               │ dns-    │   │spam- │ │dir-  │
               │ cloud-  │   │llm   │ │plugin│
               │ flare   │   │      │ │      │
               └─────────┘   └──────┘ └──────┘
                 child         child    child
                 process       process  process
```

- One child process per declared plugin instance.
- Main process has one `PluginManager` that supervises all of them.
- stdout from the plugin carries JSON-RPC responses; stdin carries requests.
- stderr is piped into the main logger, tagged with the plugin name.

## Lifecycle modes

**Long-running** (default for DNS, spam, directory): plugin started at server startup, stays resident. Multiplexes many requests on the same stdio pipe.

- Concurrency: request IDs disambiguate pipelined requests. Plugin declares max_concurrent_requests in manifest.
- Health: periodic `health()` ping (interval configurable, default 30s). On timeout, plugin restarted.

**On-demand** (default for rarely-used plugins): plugin spawned per invocation, reads request on stdin, writes response on stdout, exits.

- Simpler plugins; easier to write as shell scripts.
- Per-invocation cost (fork + exec) — ~ms. Fine for interactive operations, wrong for hot-path spam classification.
- Timeout enforced: if plugin doesn't exit within deadline, SIGTERM then SIGKILL.

Plugin declares mode in its manifest; server honors it.

## Protocol details

### Handshake

On start, server sends:
```json
{"jsonrpc":"2.0","method":"initialize","id":1,"params":{"server_version":"1.0.0","abi_version":1}}
```

Plugin responds:
```json
{"jsonrpc":"2.0","id":1,"result":{"manifest":{...}}}
```

Server validates manifest. If ABI incompatible or required capabilities missing → plugin disabled with diagnostic.

### Configuration

After handshake, server sends:
```json
{"jsonrpc":"2.0","method":"configure","id":2,"params":{"options":{"api_token":"...","zone":"example.com"}}}
```

Plugin validates against its own options_schema and responds OK or with a validation error. After `configure` succeeds, plugin is ready for type-specific calls.

### Type-specific calls

Per `requirements/11-plugins.md`:

DNS:
```json
{"jsonrpc":"2.0","method":"dns.present","id":42,
 "params":{"zone":"example.com","record_type":"TXT","name":"_acme-challenge","value":"abc123...","ttl":60}}

// response
{"jsonrpc":"2.0","id":42,"result":{"id":"cf-record-xyz"}}
```

Spam:
```json
{"jsonrpc":"2.0","method":"spam.classify","id":99,
 "params":{"envelope":{"mail_from":"...","rcpt_to":"..."},
           "headers":{...},
           "body_excerpt":"...",
           "auth_results":{...},
           "context":{...}}}

// response
{"jsonrpc":"2.0","id":99,"result":{"verdict":"spam","confidence":0.94,"reason":"Typical phishing pattern: urgent password reset + mismatched sender domain"}}
```

Event publisher:
```json
// subscribe (once, at configure time)
{"jsonrpc":"2.0","method":"events.subscribe","id":3,
 "params":{"types":["mail.received","mail.bounced","principal.auth.failure"],
           "filter":{"domain":"example.com"}}}

// publish (per event)
{"jsonrpc":"2.0","method":"events.publish","id":400,
 "params":{"event_id":"018e...","type":"mail.received","timestamp":"2026-05-10T12:34:56.789Z",
           "payload":{"message_id":"<abc@...>","rcpt":"alice@example.com","verdict":"ham"}}}

// response (ack = plugin-received; not end-to-end delivered)
{"jsonrpc":"2.0","id":400,"result":{"ack":true}}
```

The server-side event dispatcher owns a bounded in-process channel per plugin (default 1000). Events beyond capacity are dropped with a counter + warning log. Dispatch is non-blocking from the hot path.

### Plugin-to-server calls

Plugins can call back:

- `log` — emit a log line: `{"jsonrpc":"2.0","method":"log","params":{"level":"info","msg":"...","fields":{...}}}`  (notification, no id)
- `metric` — increment / observe a metric.
- `notify` — generate a server-level event (limited use; mainly for "I lost my API creds, operator needs to know").

These are fire-and-forget. Server doesn't allow arbitrary state access from plugins; those three are the full surface.

### Errors

JSON-RPC error codes:
- `-32700` Parse error (plugin must restart).
- `-32600` Invalid request.
- `-32601` Method not found (plugin didn't declare capability).
- `-32602` Invalid params.
- `-32603` Internal error.
- Plugin-defined range `-32000` to `-32099` (e.g. DNS provider returned an error).

Errors include human-readable `message` and optional `data`.

## Supervision

```
                 PluginManager
                    │
              ┌─────┴─────────────┐
              │                   │
           spawn()           monitor()
              │                   │
         per-plugin task    watchdog task
         (stdio I/O loop)   (health checks,
                             restart policy)
```

Per-plugin task:
- Reads responses from plugin stdout.
- Writes requests to plugin stdin.
- Handles request/response correlation by id.
- Enforces per-request timeout.

Watchdog task:
- Periodic health probes (long-running plugins).
- Crash detection (SIGCHLD / waitpid).
- Restart with exponential backoff: 1s, 2s, 5s, 10s, 30s, 60s cap.
- Open circuit after N crashes in M minutes (default: 5 crashes / 5 min); operator must intervene.

## Concurrency

For each long-running plugin:
- Server maintains a pool of pending requests.
- max_concurrent_requests from manifest caps outstanding.
- Beyond cap: requests queued in-server with a bounded queue (default 100); overflow returns "overloaded" error to the caller.

For on-demand plugins:
- Each invocation = one child process.
- Global cap: max N concurrent on-demand plugin processes (default 20).
- Beyond cap: queue or reject (configurable).

## Sandboxing

**Default (Linux):** use systemd's per-service hardening when running as systemd units — or, when embedded in the main service, use the `NoNewPrivileges`, `ProtectSystem=strict`, and namespace primitives via `unshare()` if the main process has CAP_SYS_ADMIN at launch.

Concretely, at v1 we do:

- Run plugin as its own UID/GID (not the server's).
- Chroot-equivalent via `ReadOnlyPaths=` and `ReadWritePaths=` when running under systemd's per-service hardening; when running plain, restrict via `chdir()` to plugin's working dir.
- No capabilities beyond what's needed (for most plugins: none).
- Network allowed (DNS plugins need it).

**Default (macOS):** `sandbox-exec` profile restricting filesystem write access to plugin dir. Network allowed.

**Default (FreeBSD):** jail if available.

Stronger isolation (seccomp filters, Linux namespaces, Wasmtime) is future work; it complicates plugin development without clear benefit at our threat model.

## Secrets handling

Plugins often need secrets (API tokens). Delivery options, in order of preference:

1. **Environment variable** — server sets env before `execve`, plugin reads `getenv`. Easy but visible in `/proc/$pid/environ` (readable by same UID; the plugin's UID, which is the only thing that sees it).
2. **Stdin at configure time** — server includes secret in the `configure` JSON-RPC params, plugin keeps in memory.
3. **FIFO / socket** — server writes secret to a named pipe, plugin reads and unlinks.

Server redacts secrets from any logging of configure params. Operator configs the secret via env var reference or external file reference (same mechanism as application-level secrets).

## Hot-path integration: spam classifier

The spam classifier sits in the delivery hot path. Key latency numbers:

- Local Ollama small model: 200-500 ms typical.
- OpenAI-compat cloud: 500-2000 ms typical.
- JSON-RPC overhead (loopback): sub-ms.

At 15 msg/min peak (scale target), concurrent classifications easily bounded. Per-plugin concurrency cap handles bursts. If plugin saturates, REQ-FILT-40 fail-open rules apply.

Per-plugin connection pooling is inside the plugin (e.g. LLM adapter maintains an HTTP keep-alive pool). Server doesn't pool JSON-RPC calls — they're already multiplexed on one stdio pipe.

## DNS plugin hot paths

DNS plugins are called at:

- ACME issuance / renewal (~minutes, rare).
- Domain add (new DKIM key → TXT record).
- DKIM rotation (new selector → TXT; remove old selector after grace period).
- Cert rotation (DANE TLSA update, if DANE is enabled for the zone).

None is latency-critical. 30s per call is fine.

DNS reconciliation: periodic `dns.list` pass to detect drift (someone deleted our DKIM TXT). If drift, log warning and optionally re-publish (configurable — operator may not want surprise writes).

## Directory plugin integration

Directory lookups are hot: every inbound recipient + every auth attempt goes through directory. Per-lookup cache (30s TTL per REQ-STORE-70 in-process caches) absorbs repeated calls.

Plugin call ordering: built-in (internal directory) → plugin chain. First authoritative answer wins.

## Delivery hook integration

`delivery.pre`: synchronous, bounded by per-call timeout (default 2s). Non-responding plugin → treated as allow (log warning). Operators who want fail-closed set `on_plugin_failure = "reject"`.

`delivery.post`: fire-and-forget. Server doesn't wait. Plugin crashes don't affect delivery.

## Configuration example

```toml
# /etc/herold/system.toml

[[plugin]]
name = "cloudflare"
path = "/var/lib/herold/plugins/herold-dns-cloudflare"
type = "dns"
lifecycle = "long-running"
options.api_token_env = "CF_API_TOKEN"

[[plugin]]
name = "route53"
path = "/var/lib/herold/plugins/herold-dns-route53"
type = "dns"
lifecycle = "long-running"
options.aws_region = "us-east-1"
options.credentials_file = "/run/secrets/aws_creds"

[[plugin]]
name = "spam-llm"
path = "/usr/lib/herold/plugins/herold-spam-llm"
type = "spam"
lifecycle = "long-running"
options.endpoint = "http://localhost:11434/v1"
options.model = "llama3.2:3b"
# no api key for local Ollama
```

In application config (DB-backed, set via CLI):

```
herold domain add example.com --dns-plugin cloudflare
herold domain add another.org --dns-plugin route53
# Both now auto-publish DKIM/MTA-STS/DMARC/TLSRPT through the right plugin.

herold spam policy set --classifier spam-llm
# All inbound messages go through this classifier.
```

## What the architecture does NOT do

- Plugin-to-plugin communication (chaining). If a DNS plugin needs to call a credential-fetcher, that's the plugin's problem.
- Shared memory between plugins and server. State transfer is through JSON-RPC params/results only.
- Plugin-defined persistent state. Plugins can keep files in their working dir; if they want structured state, they bring their own DB.
- Backpressure from plugin to server. If a plugin is slow, the server's per-plugin queue fills and overflow errors bubble up to the caller. Callers (spam, DNS publish) handle accordingly.
