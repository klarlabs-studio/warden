package domain

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConfigValidate_Diagnostics(t *testing.T) {
	cases := []struct {
		name  string
		diags map[string]string
		err   string
	}{
		{"lint", map[string]string{"lint": "out/lint.sarif"}, ""},
		{"test", map[string]string{"test": "test.sarif"}, ""},
		{"custom command step", map[string]string{"vet": "reports/vet.sarif"}, ""},
		{"security-scan has its own reader", map[string]string{"security-scan": "s.sarif"}, "only shell checks"},
		{"agent step", map[string]string{"review": "r.sarif"}, "only shell checks"},
		{"bad step name", map[string]string{"../x": "a.sarif"}, "invalid step name"},
		{"absolute", map[string]string{"lint": "/tmp/lint.sarif"}, "relative path inside the repository"},
		{"climbs out", map[string]string{"lint": "../lint.sarif"}, "relative path inside the repository"},
		{"climbs out midway", map[string]string{"lint": "a/../../lint.sarif"}, "relative path inside the repository"},
		{"unclean", map[string]string{"lint": "./lint.sarif"}, "relative path inside the repository"},
		{"backslash", map[string]string{"lint": `out\lint.sarif`}, "relative path inside the repository"},
		{"empty", map[string]string{"lint": ""}, "relative path inside the repository"},
		{"root", map[string]string{"lint": "."}, "relative path inside the repository"},
		{"trailing slash", map[string]string{"lint": "out/"}, "relative path inside the repository"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Config{Diagnostics: tc.diags}.Validate()
			switch {
			case tc.err == "" && err != nil:
				t.Fatalf("rejected: %v", err)
			case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
				t.Fatalf("error %v, want %q", err, tc.err)
			}
		})
	}
}

// A repo that does not use the key must keep its configuration digest (it is
// bound into signed execution contracts and predictions) and must not have the
// key written into its config by Save.
func TestConfigDiagnostics_AbsentChangesNothing(t *testing.T) {
	cfg := Config{Commands: map[string]string{"lint": "true"}}
	before, err := DigestJSON(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Diagnostics = map[string]string{}
	after, err := DigestJSON(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("an empty diagnostics map changed the configuration digest")
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "diagnostics") {
		t.Fatalf("an unused key was serialized:\n%s", out)
	}
	cfg.Diagnostics = map[string]string{"lint": "lint.sarif"}
	if with, _ := DigestJSON(cfg); with == before {
		t.Fatal("declaring a SARIF file must change the configuration digest")
	}
}

func TestOverlayOnto_DiagnosticsMergePerKey(t *testing.T) {
	base := Config{Diagnostics: map[string]string{"lint": "base-lint.sarif", "test": "test.sarif"}}
	child := Config{Diagnostics: map[string]string{"lint": "lint.sarif"}}
	got := child.OverlayOnto(base).Diagnostics
	if got["lint"] != "lint.sarif" || got["test"] != "test.sarif" || len(got) != 2 {
		t.Fatalf("merged %v", got)
	}
}
