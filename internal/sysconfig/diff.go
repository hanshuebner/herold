package sysconfig

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
)

// ChangeKind categorises a diff entry for the SIGHUP apply path.
type ChangeKind int

const (
	// ChangeFieldUpdate is a simple scalar update on an already-present field.
	ChangeFieldUpdate ChangeKind = iota + 1
	// ChangeListenerAdd is a new listener entry.
	ChangeListenerAdd
	// ChangeListenerRemove is a removed listener entry.
	ChangeListenerRemove
	// ChangeListenerUpdate is an in-place listener update (any field).
	ChangeListenerUpdate
	// ChangePluginAdd is a new plugin entry.
	ChangePluginAdd
	// ChangePluginRemove is a removed plugin entry.
	ChangePluginRemove
	// ChangePluginUpdate is an in-place plugin update.
	ChangePluginUpdate
)

// Change is a single typed diff entry returned by Diff.
type Change struct {
	Kind        ChangeKind
	Path        string
	Description string
}

// ErrCannotApplyLive is returned by Diff when a change cannot be applied
// without a full process restart (REQ-OPS-32).
var ErrCannotApplyLive = errors.New("sysconfig: change requires full restart")

// Diff returns a typed diff of old -> new. Changes that cannot be applied live
// (data_dir, run_as_user, run_as_group) produce an error wrapping
// ErrCannotApplyLive.
//
// Phase 1 classifies only; Phase 2+ actually applies non-trivial diffs.
func Diff(oldCfg, newCfg *Config) ([]Change, error) {
	if oldCfg == nil || newCfg == nil {
		return nil, errors.New("sysconfig: Diff requires non-nil configs")
	}

	var changes []Change

	// Non-live-applicable fields first: if any changed, refuse the whole reload.
	if oldCfg.Server.DataDir != newCfg.Server.DataDir {
		return nil, fmt.Errorf("%w: [server].data_dir changed %q -> %q", ErrCannotApplyLive, oldCfg.Server.DataDir, newCfg.Server.DataDir)
	}
	if oldCfg.Server.RunAsUser != newCfg.Server.RunAsUser {
		return nil, fmt.Errorf("%w: [server].run_as_user changed %q -> %q", ErrCannotApplyLive, oldCfg.Server.RunAsUser, newCfg.Server.RunAsUser)
	}
	if oldCfg.Server.RunAsGroup != newCfg.Server.RunAsGroup {
		return nil, fmt.Errorf("%w: [server].run_as_group changed %q -> %q", ErrCannotApplyLive, oldCfg.Server.RunAsGroup, newCfg.Server.RunAsGroup)
	}

	// Live-updatable scalars.
	if oldCfg.Server.Hostname != newCfg.Server.Hostname {
		changes = append(changes, Change{
			Kind:        ChangeFieldUpdate,
			Path:        "server.hostname",
			Description: fmt.Sprintf("%q -> %q", oldCfg.Server.Hostname, newCfg.Server.Hostname),
		})
	}
	if !reflect.DeepEqual(oldCfg.Server.AdminTLS, newCfg.Server.AdminTLS) {
		changes = append(changes, Change{
			Kind:        ChangeFieldUpdate,
			Path:        "server.admin_tls",
			Description: "admin TLS settings updated",
		})
	}
	if !reflect.DeepEqual(oldCfg.Observability, newCfg.Observability) {
		changes = append(changes, Change{
			Kind:        ChangeFieldUpdate,
			Path:        "observability",
			Description: "observability settings updated",
		})
	}
	if !reflect.DeepEqual(oldCfg.Log, newCfg.Log) {
		changes = append(changes, Change{
			Kind:        ChangeFieldUpdate,
			Path:        "log",
			Description: "log sink configuration updated",
		})
	}

	// Listeners: diff by name.
	oldL := indexListeners(oldCfg.Listener)
	newL := indexListeners(newCfg.Listener)
	names := mergedKeys(oldL, newL)
	for _, n := range names {
		o, oOK := oldL[n]
		nv, nOK := newL[n]
		switch {
		case !oOK && nOK:
			changes = append(changes, Change{
				Kind:        ChangeListenerAdd,
				Path:        "listener." + n,
				Description: fmt.Sprintf("added %s %s", nv.Protocol, nv.Address),
			})
		case oOK && !nOK:
			changes = append(changes, Change{
				Kind:        ChangeListenerRemove,
				Path:        "listener." + n,
				Description: fmt.Sprintf("removed %s %s", o.Protocol, o.Address),
			})
		case !reflect.DeepEqual(o, nv):
			changes = append(changes, Change{
				Kind:        ChangeListenerUpdate,
				Path:        "listener." + n,
				Description: "listener updated",
			})
		}
	}

	// Plugins: diff by name.
	oldP := indexPlugins(oldCfg.Plugin)
	newP := indexPlugins(newCfg.Plugin)
	pnames := mergedKeys(oldP, newP)
	for _, n := range pnames {
		_, oOK := oldP[n]
		_, nOK := newP[n]
		switch {
		case !oOK && nOK:
			changes = append(changes, Change{Kind: ChangePluginAdd, Path: "plugin." + n, Description: "plugin added"})
		case oOK && !nOK:
			changes = append(changes, Change{Kind: ChangePluginRemove, Path: "plugin." + n, Description: "plugin removed"})
		case !reflect.DeepEqual(oldP[n], newP[n]):
			changes = append(changes, Change{Kind: ChangePluginUpdate, Path: "plugin." + n, Description: "plugin updated"})
		}
	}

	return changes, nil
}

func indexListeners(ls []ListenerConfig) map[string]ListenerConfig {
	m := make(map[string]ListenerConfig, len(ls))
	for _, l := range ls {
		m[l.Name] = l
	}
	return m
}

func indexPlugins(ps []PluginConfig) map[string]PluginConfig {
	m := make(map[string]PluginConfig, len(ps))
	for _, p := range ps {
		m[p.Name] = p
	}
	return m
}

func mergedKeys[V any](a, b map[string]V) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
