package sysconfig

import "testing"

// FuzzLoad exercises the TOML parser path. The parser must not panic on any
// input; it must return an error or a valid *Config.
func FuzzLoad(f *testing.F) {
	f.Add([]byte(minimalValid))
	f.Add([]byte("not toml at all"))
	f.Add([]byte(`[server]
hostname = "a"
data_dir = "b"
totally_unknown = true
`))
	f.Add([]byte(``))
	f.Add([]byte(`[server]
hostname = 1234
`))
	// Multi-sink seeds (REQ-OPS-80..86).
	f.Add([]byte(minimalNoObs + `
[[log.sink]]
target = "stderr"
format = "auto"
level  = "info"
activities = { deny = ["poll"] }
`))
	f.Add([]byte(minimalNoObs + `
[[log.sink]]
target = "/var/log/herold/a.jsonl"
format = "json"
level  = "debug"

[[log.sink]]
target = "/var/log/herold/a.jsonl"
format = "json"
level  = "debug"
`))
	f.Add([]byte(minimalNoObs + `
[[log.sink]]
target = "relative.log"
format = "json"
level  = "info"
`))
	f.Add([]byte(minimalNoObs + `
[[log.sink]]
target = "stderr"
format = "json"
level  = "info"
activities = { allow = ["user"], deny = ["poll"] }
`))
	f.Add([]byte(minimalNoObs + `
[[log.sink]]
target = "stderr"
format = "json"
level  = "info"
activities = { deny = ["totally_invalid_activity"] }
`))

	// [clientlog] seeds (REQ-OPS-219).
	f.Add([]byte(minimalNoObs + `
[clientlog]
enabled = true
reorder_window_ms = 1000
livetail_default_duration = "15m"
livetail_max_duration = "60m"

[clientlog.defaults]
telemetry_enabled = true

[clientlog.auth]
ring_buffer_rows = 100000
ring_buffer_age  = "168h"
rate_per_session = "1000/5m"
body_max_bytes   = 262144

[clientlog.public]
enabled          = true
otlp_egress      = false
ring_buffer_rows = 10000
ring_buffer_age  = "24h"
rate_per_ip      = "10/m"
body_max_bytes   = 8192
`))
	f.Add([]byte(minimalNoObs + `
[clientlog]
enabled = false
`))
	f.Add([]byte(minimalNoObs + `
[clientlog.auth]
rate_per_session = "not-a-rate"
`))
	f.Add([]byte(minimalNoObs + `
[clientlog]
livetail_default_duration = "90m"
livetail_max_duration = "60m"
`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = Parse(raw)
	})
}
