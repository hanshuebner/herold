package plugin

import "testing"

// TestManifest_Validate_SpamRequiresPinnedTemperature covers Wave 4.1
// (REQ-FILT-12, issue #301): a spam-type plugin manifest must declare
// temperature=0, or the supervisor refuses to start it.
func TestManifest_Validate_SpamRequiresPinnedTemperature(t *testing.T) {
	base := func() Manifest {
		return Manifest{
			Name:       "p",
			Version:    "1.0.0",
			ABIVersion: ABIVersion,
			Type:       TypeSpam,
			Lifecycle:  LifecycleLongRunning,
		}
	}

	t.Run("missing temperature is rejected", func(t *testing.T) {
		m := base()
		if err := m.Validate(); err == nil {
			t.Fatal("expected error for spam manifest with no declared temperature")
		}
	})

	t.Run("non-zero temperature is rejected", func(t *testing.T) {
		m := base()
		hot := 0.7
		m.Temperature = &hot
		if err := m.Validate(); err == nil {
			t.Fatal("expected error for spam manifest with temperature != 0")
		}
	})

	t.Run("pinned temperature is accepted", func(t *testing.T) {
		m := base()
		zero := 0.0
		m.Temperature = &zero
		if err := m.Validate(); err != nil {
			t.Fatalf("unexpected error for pinned-temperature spam manifest: %v", err)
		}
	})

	t.Run("non-spam types do not require temperature", func(t *testing.T) {
		m := Manifest{
			Name:       "p",
			Version:    "1.0.0",
			ABIVersion: ABIVersion,
			Type:       TypeDNS,
			Lifecycle:  LifecycleLongRunning,
		}
		if err := m.Validate(); err != nil {
			t.Fatalf("unexpected error for non-spam manifest: %v", err)
		}
	})
}
