package domain

import "testing"

func TestPushForceMode(t *testing.T) {
	// A repo that says nothing gets the default, which is lease — see the
	// DefaultPushForce comment for why refusing is the worse default here.
	if got := (Config{}).PushForceMode(); got != ForceLease {
		t.Errorf("default PushForceMode = %q, want %q", got, ForceLease)
	}
	// An empty force field is "unset", not "invalid" — it must not shadow the
	// default with a zero value.
	if got := (Config{Push: &PushConfig{}}).PushForceMode(); got != ForceLease {
		t.Errorf("empty force = %q, want the default %q", got, ForceLease)
	}
	if got := (Config{Push: &PushConfig{Force: ForceNever}}).PushForceMode(); got != ForceNever {
		t.Errorf("PushForceMode = %q, want %q", got, ForceNever)
	}
}

// A misspelled force mode must fail at load. Silently falling back would mean a
// repo that wrote `push.force: nver` believing it forbade rewrites would have
// its history rewritten instead — the failure it was trying to prevent.
func TestConfig_ValidateRejectsUnknownPushForce(t *testing.T) {
	err := Config{Push: &PushConfig{Force: "nver"}}.Validate()
	if err == nil {
		t.Fatal("an unknown push.force must be rejected at load")
	}
	if got := err.Error(); got == "" {
		t.Error("the error must name the offending value")
	}

	for _, ok := range []PushForce{ForceLease, ForceNever, ""} {
		if err := (Config{Push: &PushConfig{Force: ok}}).Validate(); err != nil {
			t.Errorf("push.force %q should be valid: %v", ok, err)
		}
	}
	// A nil push block is valid.
	if err := (Config{}).Validate(); err != nil {
		t.Errorf("absent push block should be valid: %v", err)
	}
}

func TestPushForce_Valid(t *testing.T) {
	for _, f := range []PushForce{ForceLease, ForceNever} {
		if !f.Valid() {
			t.Errorf("%q should be valid", f)
		}
	}
	for _, f := range []PushForce{"", "force", "always", "yes"} {
		if f.Valid() {
			t.Errorf("%q should not be valid", f)
		}
	}
}
