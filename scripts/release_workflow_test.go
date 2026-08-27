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
// published, and NO SBOMs, because the tap push 401'd. For a tool whose subject
// is supply-chain provenance, losing the supply-chain artifacts to an unrelated
// credential is the wrong thing to lose.
//
// That 401 was first read as an expired PAT, by analogy with v0.19.0 and 0.20.0
// where it genuinely was one. It was not: the org secret is named
// kl_HOMEBREW_TAP_TOKEN and this workflow asked for HOMEBREW_TAP_TOKEN. An
// undefined secret expands to the empty string rather than failing, so the job
// presented NO credential and got the status that means exactly that — 401, not
// the 403 an expired-but-present token returns. The status code distinguished
// the two causes all along and was read past twice.
//
// The `major-tag` job had already learned the ordering lesson from v0.19.0,
// where a bad tap credential stranded the floating `v0` tag. The lesson simply
// never reached the step next to it. This test is what makes it reach the next
// one too.
const releaseWorkflow = "../.github/workflows/release.yml"

type ghWorkflow struct {
	Jobs map[string]struct {
		Steps []struct {
			Name string            `yaml:"name"`
			Uses string            `yaml:"uses"`
			If   string            `yaml:"if"`
			Run  string            `yaml:"run"`
			Env  map[string]string `yaml:"env"`
			With map[string]string `yaml:"with"`
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

// The tap credential must be asserted BEFORE goreleaser runs, not discovered
// from the HTTP status of a push that happens after the release is published.
//
// Both prior tap failures were diagnosed wrong from that late vantage point,
// and one of them — an undefined secret expanding to the empty string — is
// mechanically detectable a few seconds into the job. A guard that runs first
// converts a half-shipped release into a named, cheap failure.
func TestReleaseWorkflow_TapCredentialIsCheckedBeforePublishing(t *testing.T) {
	w := loadReleaseWorkflow(t)

	job, ok := w.Jobs["goreleaser"]
	if !ok {
		t.Fatal("no goreleaser job: the guard this test protects has no home")
	}

	guard, publish := -1, -1
	for i, s := range job.Steps {
		if strings.Contains(s.Uses, "goreleaser-action") && publish < 0 {
			publish = i
		}
		// The guard is identified by what it READS, not by its name, so
		// renaming the step cannot silently retire the invariant.
		if _, reads := s.Env["TAP_GITHUB_TOKEN"]; reads && s.Run != "" && guard < 0 {
			guard = i
		}
	}

	if publish < 0 {
		t.Fatal("no goreleaser-action step in the goreleaser job")
	}
	if guard < 0 {
		t.Fatal("no step reads TAP_GITHUB_TOKEN and runs a check before goreleaser; " +
			"an empty or refused tap credential would again surface as an HTTP " +
			"status from a push that happens after the release is published")
	}
	if guard > publish {
		t.Errorf("tap credential is checked at step %d, after goreleaser publishes at step %d; "+
			"a guard that runs after the release exists has nothing left to protect", guard, publish)
	}

	// Assert against the guard's CODE, not its prose. A shell comment is part
	// of the run body, so a first draft of this test was satisfied by the word
	// "403" appearing in a comment explaining the check — and passed happily
	// with the actual 403 branch deleted. Strip comments first.
	body := stripShellComments(job.Steps[guard].Run)

	// The empty case is the one that was misread, and it is the one a guard
	// checking only reachability would miss: an empty bearer token is a
	// well-formed request that GitHub answers, not a transport error.
	if !strings.Contains(body, "-z ") {
		t.Error("the tap guard does not test for an EMPTY credential; " +
			"an undefined secret expands to the empty string, which is how " +
			"v0.31.0 failed, and no reachability check will catch it")
	}
	// Case arms, not bare numbers: `403)` is a branch, `403` is a sentence.
	for _, arm := range []string{"401)", "403)"} {
		if !strings.Contains(body, arm) {
			t.Errorf("the tap guard has no %s branch; 401 means no credential was "+
				"presented (a NAME problem) and 403 means one was presented and "+
				"refused (a PERMISSION problem), and conflating them is what sent "+
				"two investigations the wrong way", strings.TrimSuffix(arm, ")"))
		}
	}
}

// stripShellComments removes whole-line `#` comments so an assertion about a
// script tests what the script DOES rather than what it says about itself.
func stripShellComments(run string) string {
	var kept []string
	for _, ln := range strings.Split(run, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}

// Every goreleaser invocation in this workflow must pin an EXACT version.
//
// Two jobs run goreleaser: one publishes the GitHub release and the Homebrew
// cask, the other builds the binaries that get packaged for npm. They produce
// artifacts for the SAME tag, so a floating range in either means one channel's
// binaries were built by a different toolchain than the other's — a difference
// that is invisible in the release notes and undetectable after the fact.
//
// The release job already carried a comment insisting on an exact version. The
// npm job, a hundred lines away, used "~> v2" — the same lesson failing to
// reach the step next to it, which is the recurring shape in this file.
func TestReleaseWorkflow_GoreleaserVersionsArePinnedExactly(t *testing.T) {
	w := loadReleaseWorkflow(t)

	seen := 0
	versions := map[string]string{}
	for jobName, job := range w.Jobs {
		for _, st := range job.Steps {
			if !strings.Contains(st.Uses, "goreleaser-action") {
				continue
			}
			seen++
			v := st.With["version"]
			switch {
			case v == "":
				t.Errorf("job %q: goreleaser-action declares no version; it would "+
					"resolve to whatever the action defaults to on the day it ran", jobName)
				continue
			// A range is anything not purely a version number. "~> v2", "latest"
			// and "v2" all resolve differently as time passes.
			case strings.ContainsAny(v, "~^*><= "), strings.EqualFold(v, "latest"):
				t.Errorf("job %q: goreleaser version %q is a range, not a pin; "+
					"a release is the artifact that most needs to be reproducible "+
					"from what this file says", jobName, v)
				continue
			}
			versions[jobName] = v
		}
	}

	if seen < 2 {
		t.Fatalf("expected at least 2 goreleaser-action steps, found %d; "+
			"this test guards the consistency BETWEEN them", seen)
	}

	// Pinned-but-different is its own defect: one tag, two builders.
	distinct := map[string]bool{}
	for _, v := range versions {
		distinct[v] = true
	}
	if len(distinct) > 1 {
		t.Errorf("goreleaser versions disagree across jobs: %v; the GitHub/Homebrew "+
			"artifacts and the npm artifacts for one tag would be built by "+
			"different toolchains", versions)
	}
}
