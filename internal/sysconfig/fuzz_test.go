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

	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = Parse(raw)
	})
}
