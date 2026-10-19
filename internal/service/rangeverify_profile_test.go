package service

import (
	"crypto/ed25519"
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// standardBase is a small .warden.yaml that loads under standard@1: the
// required steps listed, with a command for each required shell step.
const standardBase = "profile: standard\ncommands: { lint: \"true\", test: \"true\" }\n" +
	"steps: { pre_commit: [credentials], pre_push: [credentials, lint, test] }\n"

// gatedUnder returns a note for sha that conforms to standard (and strict) in
// every observation, recorded as gated under the named profile.
func gatedUnder(t *testing.T, sha, profile string) domain.RunRecord {
	t.Helper()
	rec := strictConformingRecord(t, sha)
	ref := domain.NewProfileRef(mustProfile(t, profile))
	rec.Profile = &ref
	return rec
}

// profileNames renders a result's enforced profiles with their sources, for
// comparison: "standard@1[base,flag]".
func profileNames(res RangeVerifyResult) []string {
	var out []string
	for _, p := range res.Profiles {
		var src []string
		if p.FromBase {
			src = append(src, "base")
		}
		if p.FromFlag {
			src = append(src, "flag")
		}
		out = append(out, p.Profile.String()+"["+strings.Join(src, ",")+"]")
	}
	return out
}

// Every case builds a real repository whose base commit carries baseCfg, adds
// commits on top, writes really signed notes, and runs VerifyRange. The profile
// is never passed in except as an explicit flag minimum: the base one has to be
// read from the base commit.
func TestVerifyRange_EnforcesTheBaseRefProfile(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	type commitSpec struct {
		name string
		// file, when set, is the .warden.yaml content this commit writes.
		file string
		// note returns the note for the commit, or nil for none.
		note func(t *testing.T, sha, base string) *domain.RunRecord
	}
	noted := func(profile string) func(*testing.T, string, string) *domain.RunRecord {
		return func(t *testing.T, sha, _ string) *domain.RunRecord {
			rec := sign(t, gatedUnder(t, sha, profile), pub, priv)
			return &rec
		}
	}
	spanUnder := func(profile string) func(*testing.T, string, string) *domain.RunRecord {
		return func(t *testing.T, sha, base string) *domain.RunRecord {
			rec := gatedUnder(t, sha, profile)
			rec.CoversFrom = base
			rec = sign(t, rec, pub, priv)
			return &rec
		}
	}

	tests := []struct {
		name    string
		baseCfg string // "" commits no .warden.yaml at the base
		commits []commitSpec
		opts    RangeVerifyOptions
		// want maps a commit name to its expected reason.
		want map[string]domain.VerifyReason
		// wantFailure is a substring of a named commit's profile failures.
		wantFailure  map[string]string
		wantCovered  map[string]string
		wantProfiles []string
	}{
		{
			name:         "a commit noted under permissive fails a standard base",
			baseCfg:      standardBase,
			commits:      []commitSpec{{name: "a", note: noted("permissive")}},
			opts:         RangeVerifyOptions{TrustedKeys: []string{pubB64}},
			want:         map[string]domain.VerifyReason{"a": domain.ReasonProfile},
			wantFailure:  map[string]string{"a": "profile standard@1: recorded profile ranks at least standard@1: the note was gated under permissive@1"},
			wantProfiles: []string{"standard@1[base]"},
		},
		{
			name:         "a conforming note passes a standard base",
			baseCfg:      standardBase,
			commits:      []commitSpec{{name: "a", note: noted("standard")}},
			opts:         RangeVerifyOptions{TrustedKeys: []string{pubB64}},
			want:         map[string]domain.VerifyReason{"a": domain.ReasonOK},
			wantProfiles: []string{"standard@1[base]"},
		},
		{
			name:    "a head that lowers the profile is judged by the base",
			baseCfg: standardBase,
			commits: []commitSpec{
				{name: "lower", file: "profile: permissive\n", note: noted("permissive")},
			},
			opts:         RangeVerifyOptions{TrustedKeys: []string{pubB64}},
			want:         map[string]domain.VerifyReason{"lower": domain.ReasonProfile},
			wantFailure:  map[string]string{"lower": "gated under permissive@1"},
			wantProfiles: []string{"standard@1[base]"},
		},
		{
			name:         "a head that adds a profile does not enforce it on itself",
			baseCfg:      "",
			commits:      []commitSpec{{name: "raise", file: standardBase, note: noted("permissive")}},
			opts:         RangeVerifyOptions{TrustedKeys: []string{pubB64}},
			want:         map[string]domain.VerifyReason{"raise": domain.ReasonOK},
			wantProfiles: nil,
		},
		{
			name:         "no base profile and no flag leaves the gate as it was",
			baseCfg:      "hooks: { pre_push: true }\n",
			commits:      []commitSpec{{name: "a", note: noted("permissive")}},
			opts:         RangeVerifyOptions{},
			want:         map[string]domain.VerifyReason{"a": domain.ReasonOK},
			wantProfiles: nil,
		},
		{
			name:         "a flag minimum applies without a base profile",
			baseCfg:      "",
			commits:      []commitSpec{{name: "a", note: noted("permissive")}},
			opts:         RangeVerifyOptions{TrustedKeys: []string{pubB64}, MinProfiles: []domain.ComplianceProfile{mustProfile(t, "standard")}},
			want:         map[string]domain.VerifyReason{"a": domain.ReasonProfile},
			wantFailure:  map[string]string{"a": "gated under permissive@1"},
			wantProfiles: []string{"standard@1[flag]"},
		},
		{
			name:         "a flag naming the base profile is listed once with both sources",
			baseCfg:      standardBase,
			commits:      []commitSpec{{name: "a", note: noted("standard")}},
			opts:         RangeVerifyOptions{TrustedKeys: []string{pubB64}, MinProfiles: []domain.ComplianceProfile{mustProfile(t, "standard@1")}},
			want:         map[string]domain.VerifyReason{"a": domain.ReasonOK},
			wantProfiles: []string{"standard@1[base,flag]"},
		},
		{
			name:         "a flag cannot lower the base profile",
			baseCfg:      standardBase,
			commits:      []commitSpec{{name: "a", note: noted("permissive")}},
			opts:         RangeVerifyOptions{TrustedKeys: []string{pubB64}, MinProfiles: []domain.ComplianceProfile{mustProfile(t, "permissive")}},
			want:         map[string]domain.VerifyReason{"a": domain.ReasonProfile},
			wantFailure:  map[string]string{"a": "profile standard@1:"},
			wantProfiles: []string{"standard@1[base]", "permissive@1[flag]"},
		},
		{
			name:         "a profile with no keys fails the trust clause and says why",
			baseCfg:      standardBase,
			commits:      []commitSpec{{name: "a", note: noted("standard")}},
			opts:         RangeVerifyOptions{UseRoster: true},
			want:         map[string]domain.VerifyReason{"a": domain.ReasonProfile},
			wantFailure:  map[string]string{"a": "trusted, commit-bound local provenance: no trusted key to check the signer against; a profile needs a pinned --key or a trusted_keys roster at the base ref"},
			wantProfiles: []string{"standard@1[base]"},
		},
		{
			name:         "the base roster supplies the keys a profile needs",
			baseCfg:      standardBase + "trusted_keys: [\"" + pubB64 + "\"]\n",
			commits:      []commitSpec{{name: "a", note: noted("standard")}},
			opts:         RangeVerifyOptions{UseRoster: true},
			want:         map[string]domain.VerifyReason{"a": domain.ReasonOK},
			wantProfiles: []string{"standard@1[base]"},
		},
		{
			name:    "a covering note that does not conform covers nothing",
			baseCfg: standardBase,
			commits: []commitSpec{
				{name: "a"},
				{name: "tip", note: spanUnder("permissive")},
			},
			opts:         RangeVerifyOptions{TrustedKeys: []string{pubB64}},
			want:         map[string]domain.VerifyReason{"a": domain.ReasonMissing, "tip": domain.ReasonProfile},
			wantProfiles: []string{"standard@1[base]"},
		},
		{
			name:    "a conforming covering note covers its span",
			baseCfg: standardBase,
			commits: []commitSpec{
				{name: "a"},
				{name: "tip", note: spanUnder("standard")},
			},
			opts:         RangeVerifyOptions{TrustedKeys: []string{pubB64}},
			want:         map[string]domain.VerifyReason{"a": domain.ReasonOK, "tip": domain.ReasonOK},
			wantCovered:  map[string]string{"a": "tip"},
			wantProfiles: []string{"standard@1[base]"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, svc := newRepoSvc(t)
			base := commit(t, dir, svc, "base")
			if tt.baseCfg != "" {
				base = commitConfig(t, dir, tt.baseCfg, "base config")
			}
			shas := map[string]string{}
			for _, c := range tt.commits {
				if c.file != "" {
					shas[c.name] = commitConfig(t, dir, c.file, c.name)
				} else {
					shas[c.name] = commit(t, dir, svc, c.name)
				}
				if c.note == nil {
					continue
				}
				if rec := c.note(t, shas[c.name], base); rec != nil {
					if err := svc.Repo().WriteNote(shas[c.name], *rec); err != nil {
						t.Fatal(err)
					}
				}
			}
			head := shas[tt.commits[len(tt.commits)-1].name]

			res, err := svc.VerifyRange(base, head, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := profileNames(res); !slices.Equal(got, tt.wantProfiles) {
				t.Errorf("profiles = %v, want %v", got, tt.wantProfiles)
			}
			for name, want := range tt.want {
				v := verdict(t, res, shas[name])
				if v.Reason != want {
					t.Errorf("%s reason = %q, want %q (failures %v)", name, v.Reason, want, v.ProfileFailures)
				}
				if v.ProfileUnchecked {
					t.Errorf("%s has a note; it must never be marked profile-unchecked", name)
				}
				if sub, ok := tt.wantFailure[name]; ok && !slices.ContainsFunc(v.ProfileFailures, func(f string) bool { return strings.Contains(f, sub) }) {
					t.Errorf("%s failures %v must contain %q", name, v.ProfileFailures, sub)
				}
				if want != domain.ReasonProfile && len(v.ProfileFailures) > 0 {
					t.Errorf("%s carries profile failures without ReasonProfile: %v", name, v.ProfileFailures)
				}
				if by, ok := tt.wantCovered[name]; ok && v.CoveredBy != shas[by] {
					t.Errorf("%s CoveredBy = %q, want %s", name, v.CoveredBy, by)
				}
			}
		})
	}
}

// A base config the gate cannot read is not "no profile": it fails the gate.
// Otherwise a broken or newer base config would switch enforcement off.
func TestVerifyRange_UnreadableBaseProfileFailsClosed(t *testing.T) {
	for name, cfg := range map[string]string{
		"unknown profile":  "profile: paranoid\n",
		"unknown version":  "profile: standard@2\n",
		"below its floors": "profile: standard\n",
		"does not parse":   "profile: [standard\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir, svc := newRepoSvc(t)
			commit(t, dir, svc, "root")
			base := commitConfig(t, dir, cfg, "base config")
			head := commit(t, dir, svc, "a")
			// Keyed mode, where the roster is never read: the profile still is.
			_, err := svc.VerifyRange(base, head, RangeVerifyOptions{TrustedKeys: []string{"ab"}})
			if err == nil || !strings.Contains(err.Error(), "compliance profile at base") {
				t.Fatalf("err = %v, want a failure reading the base profile", err)
			}
		})
	}
}

// A forge-accepted commit has no record to recompute. It stays accepted —
// forge.accept_authored is the base config's explicit decision — but it is
// marked, so the range's pass cannot be read as having checked it.
func TestVerifyRange_ForgeAcceptedCommitIsMarkedProfileUnchecked(t *testing.T) {
	dir, svc := newRepoSvc(t)
	commit(t, dir, svc, "root")
	fpr := gpgSigner(t, dir)

	cfg := standardBase + "forge:\n  accept_authored: true\n  keys: [\"" + fpr + "\"]\n"
	base := commitConfig(t, dir, cfg, "base: standard with forge acceptance")
	head := commit(t, dir, svc, "a commit the forge made")

	res, err := svc.VerifyRange(base, head, RangeVerifyOptions{UseRoster: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("a forge-accepted commit stays accepted under a profile: %+v", res.Commits)
	}
	v := res.Commits[0]
	if v.ForgeSigner == "" || !v.ProfileUnchecked {
		t.Errorf("verdict = %+v, want forge-accepted and marked profile-unchecked", v)
	}
	if got := profileNames(res); !slices.Equal(got, []string{"standard@1[base]"}) {
		t.Errorf("profiles = %v", got)
	}
}

// Without a profile in force, the forge-accepted verdict is exactly what it
// was: no profile mark appears where no profile applied.
func TestVerifyRange_ForgeAcceptedCommitIsUnmarkedWithoutAProfile(t *testing.T) {
	dir, svc := newRepoSvc(t)
	commit(t, dir, svc, "root")
	fpr := gpgSigner(t, dir)

	cfg := "forge:\n  accept_authored: true\n  keys: [\"" + fpr + "\"]\n"
	base := commitConfig(t, dir, cfg, "base: forge acceptance, no profile")
	head := commit(t, dir, svc, "a commit the forge made")

	res, err := svc.VerifyRange(base, head, RangeVerifyOptions{UseRoster: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() || res.Commits[0].ProfileUnchecked || len(res.Profiles) != 0 {
		t.Errorf("no profile applied, so nothing may be marked: %+v profiles %v", res.Commits, profileNames(res))
	}
}
