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

	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = Parse(raw)
	})
}
