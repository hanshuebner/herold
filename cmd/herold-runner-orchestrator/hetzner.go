package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// labelSelectorRunner scopes Hetzner queries to the VMs the
// orchestrator owns (i.e. not the bake VMs from infra/hetzner/bake.sh,
// which use herold-ci=bake instead).
const labelSelectorRunner = "herold-ci=runner"

// latestSnapshotID picks the newest snapshot tagged with our runner
// label for the given arch. This is how the orchestrator discovers
// the current image without needing a config file: bake.sh applies
// the same labels, so each Monday's fresh bake automatically becomes
// the new spawn source.
func latestSnapshotID(ctx context.Context, hc *hcloud.Client, arch string) (int64, error) {
	images, err := hc.Image.AllWithOpts(ctx, hcloud.ImageListOpts{
		ListOpts: hcloud.ListOpts{
			LabelSelector: fmt.Sprintf("herold-ci=runner,arch=%s", arch),
		},
		Type: []hcloud.ImageType{hcloud.ImageTypeSnapshot},
	})
	if err != nil {
		return 0, fmt.Errorf("hetzner image list: %w", err)
	}
	if len(images) == 0 {
		return 0, fmt.Errorf("no snapshot with labels herold-ci=runner arch=%s", arch)
	}
	sort.Slice(images, func(i, j int) bool {
		return images[i].Created.After(images[j].Created)
	})
	return images[0].ID, nil
}

// existingRunnerVMs returns all VMs in the project that the
// orchestrator owns (label herold-ci=runner, not bake). Used to
// count current pool size and to drive the reaper.
func existingRunnerVMs(ctx context.Context, hc *hcloud.Client) ([]*hcloud.Server, error) {
	return hc.Server.AllWithOpts(ctx, hcloud.ServerListOpts{
		ListOpts: hcloud.ListOpts{LabelSelector: labelSelectorRunner},
	})
}

// spawnVMArgs is everything spawnVM needs that depends on caller
// context (orchestrator config, freshly-issued runner token, the
// per-spawn unique name).
type spawnVMArgs struct {
	Name              string
	Arch              string // "arm64" | "amd64"
	ServerType        string
	Location          string
	SSHKeyName        string
	SnapshotID        int64
	RegistrationToken string
}

func spawnVM(ctx context.Context, hc *hcloud.Client, a spawnVMArgs) (*hcloud.Server, error) {
	userData, err := renderCloudInit(cloudInitParams{
		Name:              a.Name,
		Arch:              a.Arch,
		RegistrationToken: a.RegistrationToken,
	})
	if err != nil {
		return nil, fmt.Errorf("render cloud-init: %w", err)
	}

	var sshKey *hcloud.SSHKey
	if a.SSHKeyName != "" {
		sshKey, _, err = hc.SSHKey.GetByName(ctx, a.SSHKeyName)
		if err != nil {
			return nil, fmt.Errorf("get ssh key %q: %w", a.SSHKeyName, err)
		}
		if sshKey == nil {
			return nil, fmt.Errorf("ssh key %q not found", a.SSHKeyName)
		}
	}

	opts := hcloud.ServerCreateOpts{
		Name:       a.Name,
		ServerType: &hcloud.ServerType{Name: a.ServerType},
		Image:      &hcloud.Image{ID: a.SnapshotID},
		Location:   &hcloud.Location{Name: a.Location},
		UserData:   string(userData),
		Labels: map[string]string{
			"herold-ci":  "runner",
			"arch":       a.Arch,
			"spawned_at": time.Now().UTC().Format("20060102T150405Z"),
		},
	}
	if sshKey != nil {
		opts.SSHKeys = []*hcloud.SSHKey{sshKey}
	}

	result, _, err := hc.Server.Create(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("hetzner server create: %w", err)
	}
	return result.Server, nil
}

func deleteVM(ctx context.Context, hc *hcloud.Client, server *hcloud.Server) error {
	_, _, err := hc.Server.DeleteWithResult(ctx, server)
	return err
}
