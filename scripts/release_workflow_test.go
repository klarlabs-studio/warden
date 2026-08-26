package scripts_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The release workflow's steps have an ordering hazard that is invisible in the
// file: goreleaser publishes the GitHub Release and THEN pushes the Homebrew
// cask, so a stale tap credential fails the step AFTER the release already
// exists. Every later step in that job is skipped by default.
//
// v0.31.0 shipped exactly that way — binaries, checksums and a cosign bundle
// published, and NO SBOMs, because a Homebrew token had expired. For a tool
// whose subject is supply-chain provenance, losing the supply-chain artifacts
// to an unrelated credential is the wrong thing to lose.
//
// The `major-tag` job had already learned this from v0.19.0, where the same
// expired PAT stranded the floating `v0` tag. The lesson simply never reached
// the step next to it. This test is what makes it reach the next one too.
const releaseWorkflow = "../.github/workflows/release.yml"

type ghWorkflow struct {
	Jobs map[string]struct {
		Steps []struct {
			Name string `yaml:"name"`
			Uses string `yaml:"uses"`
			If   string `yaml:"if"`
			Run  string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func loadReleaseWorkflow(t *testing.T) ghWorkflow {
	t.Helper()
	b, err := os.ReadFile(releaseWorkflow)
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	var w ghWorkflow
	if err := yaml.Unmarshal(b, &w); err != nil {
		t.Fatalf("parse release workflow: %v", err)
	}
	if len(w.Jobs) == 0 {
		t.Fatal("no jobs parsed; the workflow shape changed and this check is now blind")
	}
	return w
}

// Any step that attaches an artifact to the release must survive an earlier
// step failing, because the thing most likely to fail — pushing the Homebrew
// cask — happens after the release is already published and has nothing to do
// with the artifact.
func TestReleaseWorkflow_ArtifactStepsSurviveAFailedTap(t *testing.T) {
	w := loadReleaseWorkflow(t)
	job, ok := w.Jobs["goreleaser"]
	if !ok {
		t.Fatal("no goreleaser job; the workflow was restructured")
	}

	var checked int
	for _, s := range job.Steps {
		if !strings.Contains(s.Run, "gh release upload") {
			continue
		}
		checked++
		if !strings.Contains(s.If, "always()") {
			t.Errorf("step %q uploads a release asset but has if: %q.\n"+
				"  Without always() it is skipped when an EARLIER step fails — and the step most\n"+
				"  likely to fail (the brew cask push) runs AFTER the release is published, so the\n"+
				"  asset is lost to a credential that has nothing to do with it. See v0.31.0.",
				s.Name, s.If)
		}
		// always() alone would also run on a cancelled workflow, uploading from
		// a run somebody deliberately stopped.
		if !strings.Contains(s.If, "cancelled()") {
			t.Errorf("step %q runs on always() but does not exclude cancelled(); "+
				"a cancelled run should not still publish artifacts", s.Name)
		}
		// Running unconditionally is only safe if it checks that the release
		// exists — otherwise a goreleaser that died BEFORE publishing produces a
		// second, confusing failure on top of the real one.
		if !strings.Contains(s.Run, "gh release view") {
			t.Errorf("step %q runs unconditionally but never checks the release exists; "+
				"decide from the RELEASE, not from the job's exit code", s.Name)
		}
	}
	if checked == 0 {
		t.Fatal("no release-upload step found; either the workflow changed or this check is blind")
	}
}

// A step that tolerates a tool's exit code must verify the tool's OUTPUT, or a
// silent no-op uploads nothing and reports success. `nox scan` exits non-zero on
// findings even when baselined, so its exit code is deliberately ignored — which
// makes checking the artifacts the only thing standing between a green release
// and an empty SBOM.
func TestReleaseWorkflow_ToleratedExitStillChecksTheArtifact(t *testing.T) {
	w := loadReleaseWorkflow(t)
	for _, job := range w.Jobs {
		for _, s := range job.Steps {
			if !strings.Contains(s.Run, "|| true") {
				continue
			}
			if !strings.Contains(s.Run, "test -s") {
				t.Errorf("step %q swallows a command's exit code with `|| true` but never "+
					"checks it produced anything. Assert the artifact, not the exit status.", s.Name)
			}
		}
	}
}

// A release must be re-drivable. `workflow_dispatch` exists on this workflow
// precisely so a run can be repeated when a later channel fails — the tap
// credential expiring is the recurring case — and a step that errors merely
// because its work is ALREADY DONE turns every recovery into a red run.
//
// v0.31.0 is the worked example. The re-drive repaired the Homebrew tap and
// attached the missing SBOMs, and the workflow still reported failure, because
// npm versions are immutable and the packages were already published. Anyone
// reading the run status would have gone looking for a problem that had just
// been fixed.
func TestReleaseWorkflow_PublishStepsAreRedrivable(t *testing.T) {
	w := loadReleaseWorkflow(t)
	var checked int
	for _, job := range w.Jobs {
		for _, s := range job.Steps {
			if !strings.Contains(s.Run, "npm publish") {
				continue
			}
			checked++
			// The guard must consult the REGISTRY. Anything cheaper — a `|| true`,
			// or grepping npm's error text — either swallows real failures or
			// depends on wording npm can change.
			if !strings.Contains(s.Run, "npm view") {
				t.Errorf("step %q publishes to npm without first checking whether the version "+
					"is already there.\n  Re-driving a release then fails on npm's immutability and "+
					"reports the whole run red with every channel correct. See v0.31.0.", s.Name)
			}
			// And it must not reach for the blunt instrument: `|| true` on a
			// publish hides authentication and OIDC failures too, which is how a
			// channel silently stops shipping.
			if strings.Contains(s.Run, "npm publish --provenance --access public || true") {
				t.Errorf("step %q swallows every publish failure, not just the already-published "+
					"case; an auth or OIDC failure would report success", s.Name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no npm publish step found; either the workflow changed or this check is blind")
	}
}
