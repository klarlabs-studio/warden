package domain

import "testing"

// The whole policy rests on this comparison, so it is the one to get wrong.
func TestForgeKeyMatches(t *testing.T) {
	const gh = "968479A1AFF927E37D1A566BB5690EEEBB952194"
	cases := []struct {
		name        string
		fingerprint string
		pinned      []string
		want        bool
	}{
		{"exact", gh, GitHubWebFlowKeys, true},
		{"lowercase from git", "968479a1aff927e37d1a566bb5690eeebb952194", GitHubWebFlowKeys, true},
		{"the other web-flow key", "5DE3E0509C47EA3CF04A42D34AEE18F83AFDEB23", GitHubWebFlowKeys, true},
		{"padded", "  " + gh + "  ", GitHubWebFlowKeys, true},
		{"a different key", "0000000000000000000000000000000000000000", GitHubWebFlowKeys, false},
		{"no pinned keys means nothing matches", gh, nil, false},

		// The security cases. An empty fingerprint is what CommitSignature
		// returns when git could NOT verify the signature, so matching it would
		// turn "the key could not be checked" into "the forge signed this" —
		// the exact substitution this policy exists to refuse.
		{"empty never matches", "", GitHubWebFlowKeys, false},
		{"whitespace never matches", "   ", GitHubWebFlowKeys, false},

		// A 64-bit key id is the tail of the fingerprint and travels inside the
		// signature packet. Accepting it would let the commit name its own
		// inspector.
		{"short key id is not a fingerprint", "B5690EEEBB952194", GitHubWebFlowKeys, false},
		{"fingerprint prefix is not a match", "968479A1AFF927E37D1A566B", GitHubWebFlowKeys, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ForgeKeyMatches(c.fingerprint, c.pinned); got != c.want {
				t.Errorf("ForgeKeyMatches(%q) = %v, want %v", c.fingerprint, got, c.want)
			}
		})
	}
}

// The zero value must reject. An upgrade that silently began accepting un-noted
// commits would weaken every deployed gate without anyone choosing it — the
// reasoning ADR 0003 already settled for external attestations.
func TestForgeConfig_ZeroValueRejects(t *testing.T) {
	var cfg ForgeConfig
	if cfg.Policy() != ForgeReject {
		t.Error("the zero ForgeConfig must reject forge-authored commits")
	}
	var p ForgePolicy
	if p != ForgeReject {
		t.Error("the zero ForgePolicy must be ForgeReject")
	}
}

func TestForgeConfig_AcceptAndKeyDefaulting(t *testing.T) {
	cfg := ForgeConfig{AcceptAuthored: true}
	if cfg.Policy() != ForgeAccept {
		t.Error("accept_authored: true must select ForgeAccept")
	}
	// Defaulting to GitHub's keys is what makes the setting usable without
	// asking every repository to paste a fingerprint it cannot verify.
	if got := cfg.ForgeKeys(); len(got) != len(GitHubWebFlowKeys) {
		t.Errorf("default keys = %v, want GitHub's web-flow keys", got)
	}
	custom := ForgeConfig{AcceptAuthored: true, Keys: []string{"AAAA"}}
	if got := custom.ForgeKeys(); len(got) != 1 || got[0] != "AAAA" {
		t.Errorf("explicit keys must win, got %v", got)
	}
}

// A pinned fingerprint that is not a fingerprint is a configuration error the
// operator should be able to see the consequence of: it simply never matches,
// so the gate stays closed rather than opening on a typo.
func TestForgeKeyMatches_MalformedPinDoesNotOpenTheGate(t *testing.T) {
	for _, pin := range [][]string{{""}, {"   "}, {"not-a-fingerprint"}, {"B5690EEEBB952194"}} {
		if ForgeKeyMatches("968479A1AFF927E37D1A566BB5690EEEBB952194", pin) {
			t.Errorf("pin %q should not match a real fingerprint", pin)
		}
	}
}
