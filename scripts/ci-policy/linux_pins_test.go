package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/config"
)

// Independent publication receipts are pinned separately from the candidate
// records. Catch drift between the remote command, local config and consumer.
func TestPinnedLinuxPolicies(t *testing.T) {
	root := filepath.Join("..", "..", ".github")
	workflow, err := os.ReadFile(filepath.Join(root, "workflows", "linux-test-contract.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"sudo chown -R 10001:10001",
		"--network none --user 10001:10001 --env HOME=/tmp",
		"cosign verify --certificate-identity",
	} {
		if !strings.Contains(string(workflow), required) {
			t.Fatalf("remote workflow lost non-root signed-image validation %q", required)
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			dir := filepath.Join(root, "warden", "linux-test", "profiles", arch)
			cfg, err := config.NewRepository(dir).Load()
			if err != nil {
				t.Fatal(err)
			}
			var receipt struct {
				Source    string `json:"source_commit"`
				Platform  string `json:"platform"`
				Reference string `json:"registry_reference"`
				ConfigID  string `json:"docker_config_id"`
			}
			readJSON := func(name string, target any) {
				t.Helper()
				data, e := os.ReadFile(filepath.Join(dir, name))
				if e != nil {
					t.Fatal(e)
				}
				if e = json.Unmarshal(data, target); e != nil {
					t.Fatal(e)
				}
			}
			readJSON("publication.json", &receipt)
			if receipt.Source != "ba1e8e1d0c42782328e400911c3e30cf5011664a" ||
				receipt.Platform != "linux/"+arch || cfg.Execution.Image != receipt.Reference ||
				!domain.ValidContainerImageID(receipt.ConfigID) {
				t.Fatal("publication and local environment differ")
			}
			if cfg.Execution.Mode != "container" || cfg.Execution.Runtime != "auto" ||
				cfg.Execution.Platform != receipt.Platform || cfg.Execution.Network != "none" {
				t.Fatal("local execution no longer matches the offline contract")
			}
			digest, err := domain.DigestJSON(cfg)
			if err != nil {
				t.Fatal(err)
			}
			files := []string{"docker-policy-template.json"}
			if arch == "arm64" {
				files = append(files, "apple-policy-template.json")
			}
			for _, name := range files {
				var p domain.CIJobPolicy
				readJSON(name, &p)
				if len(p.Jobs) != 1 || p.Jobs["test"] == nil || len(p.Jobs["test"].Steps) != 1 {
					t.Fatal("unexpected policy inventory")
				}
				s := p.Jobs["test"].Steps[domain.StepTest]
				if s.ConfigurationDigest != digest || s.Command != cfg.Commands["test"] ||
					s.Platform != receipt.Platform || s.Network != "none" || s.ContextDigest != "" || s.Inputs != nil {
					t.Fatal("protected template no longer matches effective configuration")
				}
				wantID, wantKind := receipt.ConfigID, "config"
				if name == "apple-policy-template.json" {
					_, wantID, _ = strings.Cut(receipt.Reference, "@")
					wantKind = "manifest"
				}
				if s.ImageID != wantID || s.ImageIDKind != wantKind {
					t.Fatal("image identities were conflated")
				}
				// Derived fields are supplied per candidate, never copied from a note.
				s.ContextDigest = domain.DigestBytes([]byte("independent context"))
				s.Inputs = map[string]string{}
				p.Jobs["test"].Steps[domain.StepTest] = s
				if err := p.Validate(); err != nil {
					t.Fatal(err)
				}
			}
			for _, exact := range []string{receipt.Reference, receipt.ConfigID, cfg.Commands["test"]} {
				if !strings.Contains(string(workflow), exact) {
					t.Fatalf("remote workflow lost pin %q", exact)
				}
			}
		})
	}
}
