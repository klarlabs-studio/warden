package scripts_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The GitLab component and its docs name the warden release a consumer gets by
// default. That version is bumped by hand in the release-prep commit, in four
// files, and nothing checked it: the bump used to land as a separate follow-up
// PR days after the tag, so for that window every GitLab consumer was pinned to
// the previous release while the changelog said otherwise.
//
// The invariant is "the default is the newest released version in the
// changelog", which holds both between releases and in the release-prep commit,
// so it can be asserted on every PR.
var releaseHeading = regexp.MustCompile(`^## \[(\d+\.\d+\.\d+)\]`)

func newestReleasedVersion(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(changelogPath)
	if err != nil {
		t.Fatalf("read changelog: %v", err)
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if m := releaseHeading.FindStringSubmatch(line); m != nil {
			return m[1] // the first numbered heading; [Unreleased] does not match
		}
	}
	t.Fatal("no numbered release heading found; the changelog format changed and this check is now blind")
	return ""
}

func TestGitLabDefaultIsTheNewestReleasedVersion(t *testing.T) {
	want := newestReleasedVersion(t)

	raw, err := os.ReadFile("../" + gitlabTemplatePath)
	if err != nil {
		t.Fatal(err)
	}
	var tpl struct {
		Spec struct {
			Inputs map[string]glInput `yaml:"inputs"`
		} `yaml:"spec"`
	}
	// The component file is two YAML documents: the spec header, then the jobs.
	if err := yaml.Unmarshal(raw, &tpl); err != nil {
		t.Fatalf("parse %s: %v", gitlabTemplatePath, err)
	}
	in, ok := tpl.Spec.Inputs["warden-version"]
	if !ok || in.Default == nil {
		t.Fatal("templates/verify.yml has no warden-version default; this check is now blind")
	}
	if got := *in.Default; got != want {
		t.Errorf("templates/verify.yml defaults warden-version to %s, but the newest released version is %s", got, want)
	}

	// The two docs repeat the default in prose; a drifted doc misleads a consumer
	// even when the template is right.
	for path, needle := range map[string]string{
		"../docs/gitlab-ci.md":        "| `warden-version` | `" + want + "` |",
		"../docs/ci-job-contracts.md": "The component defaults to v" + want + ";",
	} {
		doc, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(doc), needle) {
			t.Errorf("%s does not state the default as %s (looked for %q)", path, want, needle)
		}
	}
}
