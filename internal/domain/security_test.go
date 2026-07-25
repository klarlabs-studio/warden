package domain

import "testing"

func TestSecurityScanConfig_ResolvedModeDefaultsToDelta(t *testing.T) {
	cases := []struct {
		name string
		cfg  SecurityScanConfig
		want ScanMode
	}{
		{"unset defaults to delta", SecurityScanConfig{}, ScanModeDelta},
		{"explicit delta", SecurityScanConfig{Mode: ScanModeDelta}, ScanModeDelta},
		{"explicit total", SecurityScanConfig{Mode: ScanModeTotal}, ScanModeTotal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.cfg.ResolvedMode(); got != c.want {
				t.Errorf("ResolvedMode() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSecurityScanConfig_Validate(t *testing.T) {
	for _, mode := range []ScanMode{"", ScanModeDelta, ScanModeTotal} {
		if err := (SecurityScanConfig{Mode: mode}).Validate(); err != nil {
			t.Errorf("mode %q: unexpected error %v", mode, err)
		}
	}
	// A typo must be rejected at load, not silently resolved to the default:
	// the setting decides how strict the security gate is.
	if err := (SecurityScanConfig{Mode: "dela"}).Validate(); err == nil {
		t.Error("a misspelled mode was accepted; it would silently gate on the default")
	}
}

func TestSecurityScanConfig_VersionCheckEnabledByDefault(t *testing.T) {
	if !(SecurityScanConfig{}).VersionCheckEnabled() {
		t.Error("scanner version drift detection must be on unless opted out")
	}
	off := false
	if (SecurityScanConfig{VersionCheck: &off}).VersionCheckEnabled() {
		t.Error("version_check: false must switch the check off")
	}
	on := true
	if !(SecurityScanConfig{VersionCheck: &on}).VersionCheckEnabled() {
		t.Error("version_check: true must switch the check on")
	}
}

func TestMergeSecurityScan(t *testing.T) {
	off := false

	t.Run("child fields overlay one at a time", func(t *testing.T) {
		base := SecurityScanConfig{Mode: ScanModeDelta, PinFile: ".github/workflows/ci.yml"}
		got := MergeSecurityScan(base, SecurityScanConfig{Base: "origin/main"})
		if got.PinFile != base.PinFile {
			t.Errorf("pin_file = %q, want the base's %q — a child setting one field must not drop the others",
				got.PinFile, base.PinFile)
		}
		if got.Base != "origin/main" {
			t.Errorf("base = %q, want origin/main", got.Base)
		}
	})

	t.Run("a child may not relax an org base from total to delta", func(t *testing.T) {
		got := MergeSecurityScan(SecurityScanConfig{Mode: ScanModeTotal}, SecurityScanConfig{Mode: ScanModeDelta})
		if got.Mode != ScanModeTotal {
			t.Errorf("mode = %q, want total: a repo must not be able to silently weaken an inherited gate", got.Mode)
		}
	})

	t.Run("a child may tighten delta to total", func(t *testing.T) {
		got := MergeSecurityScan(SecurityScanConfig{Mode: ScanModeDelta}, SecurityScanConfig{Mode: ScanModeTotal})
		if got.Mode != ScanModeTotal {
			t.Errorf("mode = %q, want total", got.Mode)
		}
	})

	t.Run("version_check is the child's to turn off", func(t *testing.T) {
		got := MergeSecurityScan(SecurityScanConfig{}, SecurityScanConfig{VersionCheck: &off})
		if got.VersionCheckEnabled() {
			t.Error("a repo whose pin lives somewhere warden cannot read must be able to disable the check")
		}
	})

	t.Run("an unset child changes nothing", func(t *testing.T) {
		base := SecurityScanConfig{Mode: ScanModeTotal, Base: "origin/main", PinFile: "x.yml", VersionCheck: &off}
		if got := MergeSecurityScan(base, SecurityScanConfig{}); got != base {
			t.Errorf("MergeSecurityScan(base, zero) = %+v, want %+v", got, base)
		}
	})
}

func TestSecurityScanConfig_IsZero(t *testing.T) {
	if !(SecurityScanConfig{}).IsZero() {
		t.Error("the zero config must report itself as unset")
	}
	if (SecurityScanConfig{Mode: ScanModeDelta}).IsZero() {
		t.Error("an explicitly set mode is not unset")
	}
}
