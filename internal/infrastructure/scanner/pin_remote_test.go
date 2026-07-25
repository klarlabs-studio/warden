package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseRemoteSpec(t *testing.T) {
	spec, ok := ParseRemoteSpec("klarlabs-studio/.github/.github/workflows/go-ci.yml@main")
	if !ok {
		t.Fatal("a fleet-shared workflow reference must parse")
	}
	if spec.Owner != "klarlabs-studio" || spec.Repo != ".github" ||
		spec.Path != ".github/workflows/go-ci.yml" || spec.Ref != "main" {
		t.Errorf("parsed = %+v", spec)
	}
	if got := spec.rawURL(); got != "https://raw.githubusercontent.com/klarlabs-studio/.github/main/.github/workflows/go-ci.yml" {
		t.Errorf("rawURL = %q", got)
	}

	// A repo-relative path must NOT be mistaken for a remote one — that would
	// turn an ordinary pin_file into a network call.
	for _, local := range []string{
		".github/workflows/ci.yml",
		"ci.yml",
		"deep/nested/path/ci.yaml",
		"",                    // unset
		"owner/repo/file.yml", // no @ref
	} {
		if _, ok := ParseRemoteSpec(local); ok {
			t.Errorf("ParseRemoteSpec(%q) parsed as remote; @ref is the discriminator", local)
		}
	}
}

// stubFetch replaces the network for a test and isolates the cache, so a run
// cannot be satisfied by a real fetch or by another test's cached pin.
func stubFetch(t *testing.T, body string, err error) *int {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // Linux
	t.Setenv("HOME", t.TempDir())           // macOS UserCacheDir
	calls := 0
	orig := httpGet
	t.Cleanup(func() { httpGet = orig })
	httpGet = func(context.Context, string) ([]byte, error) {
		calls++
		if err != nil {
			return nil, err
		}
		return []byte(body), nil
	}
	return &calls
}

const sharedWorkflow = `
name: Go CI
on: [push]
jobs:
  test:
    uses: ./x.yml
    with:
      nox-version: "1.16.1"
`

func TestDiscoverPin_Remote(t *testing.T) {
	calls := stubFetch(t, sharedWorkflow, nil)
	spec := "klarlabs-studio/.github/.github/workflows/go-ci.yml@main"

	pin, found, err := DiscoverPin(context.Background(), t.TempDir(), "nox", spec)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("a pin in the shared workflow must be found")
	}
	if pin.Version != "1.16.1" {
		t.Errorf("Version = %q, want 1.16.1", pin.Version)
	}
	// The source must name the remote spec, so the message points at the file
	// that actually has to change.
	if pin.Source != spec {
		t.Errorf("Source = %q, want the remote spec", pin.Source)
	}

	// Second call is served from cache: a pre-push gate must not hit the network
	// on every push.
	if _, _, err := DiscoverPin(context.Background(), t.TempDir(), "nox", spec); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Errorf("fetched %d times, want 1 — the resolved pin should be cached", *calls)
	}
}

// Every remote failure must degrade to "no pin", never to an error: none of
// these mean the developer's change is wrong, and none may fail a push.
func TestDiscoverPin_RemoteFailuresNeverFailThePush(t *testing.T) {
	cases := map[string]struct {
		body string
		err  error
	}{
		"network unreachable":  {"", errors.New("dial tcp: no route to host")},
		"404 moved or renamed": {"", errors.New("GET ...: 404 Not Found")},
		"workflow has no pin":  {"name: CI\non: [push]\njobs: {}\n", nil},
		"not even valid YAML":  {":::not yaml:::", nil},
		"empty response":       {"", nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stubFetch(t, tc.body, tc.err)
			pin, found, err := DiscoverPin(context.Background(),
				t.TempDir(), "nox", "o/r/.github/workflows/ci.yml@main")
			if err != nil {
				t.Errorf("err = %v, want nil — a push must not fail on this", err)
			}
			if found {
				t.Errorf("found = true (%+v), want no pin", pin)
			}
		})
	}
}

// A local pin_file must still be read from disk, with no network involved.
func TestDiscoverPin_LocalPathUnaffected(t *testing.T) {
	calls := stubFetch(t, sharedWorkflow, nil)
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ci.yml"),
		[]byte("name: CI\non: [push]\nenv:\n  NOX_VERSION: \"1.2.3\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pin, found, err := DiscoverPin(context.Background(), root, "nox", ".github/workflows/ci.yml")
	if err != nil || !found {
		t.Fatalf("local pin: found=%v err=%v", found, err)
	}
	if pin.Version != "1.2.3" {
		t.Errorf("Version = %q, want the local pin", pin.Version)
	}
	if *calls != 0 {
		t.Errorf("made %d network calls for a local pin_file, want 0", *calls)
	}
	if strings.Contains(pin.Source, "@") {
		t.Errorf("Source = %q, want the repo-relative path", pin.Source)
	}
}

// The shape a fleet's shared workflow actually uses: the pin is the DEFAULT of
// the workflow's own input declaration, not a scalar. Reading only scalars meant
// warden could see a caller that overrode the pin but never the definition that
// set it — so a centrally-pinned fleet had no discoverable pin at all (#112).
func TestDiscoverPin_ReusableWorkflowInputDefault(t *testing.T) {
	stubFetch(t, `
name: Go CI
on:
  workflow_call:
    inputs:
      nox-version:
        description: "nox release version (no leading v)."
        type: string
        default: "1.16.1"
      nox-sha256:
        type: string
        default: "e0d7edf5"
jobs:
  security:
    runs-on: ubuntu-latest
`, nil)
	pin, found, err := DiscoverPin(context.Background(), t.TempDir(), "nox",
		"klarlabs-studio/.github/.github/workflows/go-ci.yml@main")
	if err != nil || !found {
		t.Fatalf("input default must be discoverable: found=%v err=%v", found, err)
	}
	if pin.Version != "1.16.1" {
		t.Errorf("Version = %q, want the input's default", pin.Version)
	}
}

func TestScalarOrDefault(t *testing.T) {
	cases := map[string]string{
		`v: "1.2.3"`: "1.2.3",
		"v:\n  description: x\n  default: \"4.5\"": "4.5",
		"v:\n  description: x\n  type: string":     "", // declared, not pinned
		"v: [1, 2]":                                "", // a list pins nothing
		"v:\n  default:\n    nested: 1":            "", // non-scalar default
	}
	for src, want := range cases {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		// doc -> document -> mapping -> value of "v"
		val := doc.Content[0].Content[1]
		if got := scalarOrDefault(val); got != want {
			t.Errorf("scalarOrDefault(%q) = %q, want %q", src, got, want)
		}
	}
}
