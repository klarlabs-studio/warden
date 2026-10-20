package steps

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/proc"
)

const sweepNonce = "0123456789abcdef0123456789abcdef"

// legacyHostLabel is the owner label releases before the machine id wrote.
const legacyHostLabel = "de.klarlabs.warden.host"

// thisMachine is the machine id this test process labels and sweeps by, in a
// config dir of the test's own.
func thisMachine(t *testing.T) string {
	t.Helper()
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	id, ok := proc.MachineID()
	if !ok {
		t.Fatal("no machine id")
	}
	return id
}

// exitedProcess is the pid of a process that has already exited.
func exitedProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// runningProcess is the pid of a process that outlives the test's sweep.
func runningProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

func sweepName(pid int) string { return "warden-" + strconv.Itoa(pid) + "-" + sweepNonce }

// fakeSweepEngine installs a CLI named bin that answers its listing command
// with listing and records every call, one line of NUL-free arguments each.
func fakeSweepEngine(t *testing.T, bin, listing string) (calls func() []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI uses POSIX shell")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	listingFile := filepath.Join(dir, "listing")
	if err := os.WriteFile(listingFile, []byte(listing), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
echo "$*" >> ` + shellQuote(log) + `
case "$1" in
ps|list) cat ` + shellQuote(listingFile) + ` ;;
rm|delete) exit 0 ;;
*) exit 90 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, bin), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		data, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
}

func removals(calls []string) []string {
	var out []string
	for _, c := range calls {
		if name, ok := strings.CutPrefix(c, "rm --force "); ok {
			out = append(out, name)
		} else if name, ok := strings.CutPrefix(c, "delete --force "); ok {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func TestOwnerFromName(t *testing.T) {
	for name, want := range map[string]int{
		sweepName(42):                              42,
		"warden-" + sweepNonce:                     0, // an older warden's name carries no owner
		"warden-42-" + sweepNonce[:31]:             0,
		"warden-42-" + strings.ToUpper(sweepNonce): 0,
		"warden-042-" + sweepNonce:                 0,
		"warden--42-" + sweepNonce:                 0,
		"warden-0-" + sweepNonce:                   0,
		"other-42-" + sweepNonce:                   0,
		"warden-x-" + sweepNonce:                   0,
	} {
		got, ok := ownerFromName(name)
		if ok != (want != 0) || got != want {
			t.Errorf("ownerFromName(%q) = %d, %v; want %d", name, got, ok, want)
		}
	}
}

// A Docker-compatible engine lists only this machine's warden containers
// (the label filter), and the sweep removes exactly those whose owner has
// exited: never this process's, never a running owner's, never a name it
// cannot read an owner from.
func TestSweepCompatRemovesOnlyDeadOwners(t *testing.T) {
	machine := thisMachine(t)
	dead, alive := exitedProcess(t), runningProcess(t)
	listing := strings.Join([]string{sweepName(dead), sweepName(alive), sweepName(os.Getpid()), "warden-" + sweepNonce, "someone-elses"}, "\n") + "\n"
	calls := fakeSweepEngine(t, "podman", listing)
	removed := SweepOrphanContainers(context.Background(), domain.ExecutionConfig{Image: "checks:dev", Runtime: "podman"})
	if want := []string{sweepName(dead)}; !slices.Equal(removed, want) || !slices.Equal(removals(calls()), want) {
		t.Fatalf("removed %v (calls %q), want %v", removed, calls(), want)
	}
	// The engine is asked for this machine's id, never for a host name: on a
	// shared engine a namesake machine's containers are not even listed.
	if got := calls()[0]; got != "ps --all --filter label="+ownerMachineLabel+"="+machine+" --format {{.Names}}" {
		t.Fatalf("listing call %q", got)
	}
	// Once per engine per process: a second sweep lists nothing.
	before := len(calls())
	if SweepOrphanContainers(context.Background(), domain.ExecutionConfig{Image: "checks:dev", Runtime: "podman"}); len(calls()) != before {
		t.Fatalf("the engine was swept twice: %q", calls())
	}
}

// Apple's CLI cannot filter by label, so the sweep reads the labels itself:
// another machine's container (even one with this machine's host name), one
// labeled only with the host name earlier releases wrote, an unlabeled one,
// and one whose pid label disagrees with its name are all left alone.
func TestSweepAppleRemovesOnlyDeadOwnersOnThisMachine(t *testing.T) {
	machine := thisMachine(t)
	host, _ := os.Hostname()
	dead, alive := exitedProcess(t), runningProcess(t)
	entry := func(name string, labels map[string]string) string {
		var parts []string
		for k, v := range labels {
			parts = append(parts, strconv.Quote(k)+":"+strconv.Quote(v))
		}
		return `{"configuration":{"id":` + strconv.Quote(name) + `,"labels":{` + strings.Join(parts, ",") + `}},"status":{"state":"stopped"}}`
	}
	own := func(pid int) map[string]string {
		return map[string]string{ownerPIDLabel: strconv.Itoa(pid), ownerMachineLabel: machine}
	}
	namesake := func(pid int) map[string]string {
		return map[string]string{ownerPIDLabel: strconv.Itoa(pid), ownerMachineLabel: strings.Repeat("f", 32), legacyHostLabel: host}
	}
	listing := "[" + strings.Join([]string{
		entry(sweepName(dead), own(dead)),
		entry(sweepName(alive), own(alive)),
		entry(sweepName(os.Getpid()), own(os.Getpid())),
		entry(sweepName(dead+1000000), namesake(dead+1000000)),
		entry(sweepName(dead+2000000), nil),
		entry(sweepName(dead+3000000), map[string]string{ownerPIDLabel: strconv.Itoa(alive), ownerMachineLabel: machine}),
		entry("warden-"+sweepNonce, map[string]string{ownerMachineLabel: machine}),
		entry(sweepName(dead+4000000), map[string]string{ownerPIDLabel: strconv.Itoa(dead + 4000000), legacyHostLabel: host}),
	}, ",") + "]"
	calls := fakeSweepEngine(t, "container", listing)
	removed := SweepOrphanContainers(context.Background(), domain.ExecutionConfig{Image: "checks:dev", Runtime: "apple"})
	if want := []string{sweepName(dead)}; !slices.Equal(removed, want) || !slices.Equal(removals(calls()), want) {
		t.Fatalf("removed %v (calls %q), want %v", removed, calls(), want)
	}
	if got := calls()[0]; got != "list --all --format json" {
		t.Fatalf("listing call %q", got)
	}
}

// An engine that is down, or answers in a shape the sweep does not know,
// removes nothing.
func TestSweepIgnoresUnreadableListings(t *testing.T) {
	thisMachine(t)
	calls := fakeSweepEngine(t, "container", "not json")
	if removed := SweepOrphanContainers(context.Background(), domain.ExecutionConfig{Image: "checks:dev", Runtime: "apple"}); len(removed) != 0 || len(removals(calls())) != 0 {
		t.Fatalf("removed %v from an unreadable listing", removed)
	}
	if SweepOrphanContainers(context.Background(), domain.ExecutionConfig{Mode: "host"}) != nil {
		t.Fatal("host execution swept an engine")
	}
}

// The gate's sweep runs once per process, in the background.
func TestSweepSelectedInBackground(t *testing.T) {
	thisMachine(t)
	dead := exitedProcess(t)
	calls := fakeSweepEngine(t, "nerdctl", sweepName(dead)+"\n")
	bin, err := exec.LookPath("nerdctl")
	if err != nil {
		t.Fatal(err)
	}
	selected := &application.ContainerExecution{Binary: bin, Environment: domain.ExecutionEnvironment{Runtime: "nerdctl"}}
	SweepSelectedInBackground(selected)
	SweepSelectedInBackground(selected)
	SweepSelectedInBackground(nil)
	waitFor(t, func() bool { return len(removals(calls())) == 1 })
	if got := calls(); len(got) != 2 || removals(got)[0] != sweepName(dead) {
		t.Fatalf("calls %q", got)
	}
}

// Every container warden starts names its owner and carries the labels the
// sweep reads, on both kinds of backend.
func TestContainersCarryOwnerLabels(t *testing.T) {
	machine := thisMachine(t)
	want := []string{"--label", ownerPIDLabel + "=" + strconv.Itoa(os.Getpid()), "--label", ownerMachineLabel + "=" + machine}
	if got := labelArgs(); !slices.Equal(got, want) {
		t.Fatalf("label args %q, want %q", got, want)
	}
	if pid, ok := ownerFromName(containerName(sweepNonce)); !ok || pid != os.Getpid() {
		t.Fatalf("container name %q does not name this process", containerName(sweepNonce))
	}

	fakeAppleEngine(t)
	selected, err := ResolveContainer(domain.ExecutionConfig{Runtime: "apple", Image: "checks:dev"})
	if err != nil {
		t.Fatal(err)
	}
	sc := application.StepContext{WorktreeDir: t.TempDir(), Hook: domain.PreCommit, Container: selected, Commands: map[string]string{"test": "true"}}
	if _, err := NewShellStep(domain.StepTest, "test").Run(context.Background(), sc); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(os.Getenv("APPLE_CALLS") + ".args")
	if err != nil {
		t.Fatal(err)
	}
	created := strings.Split(string(args), "\x00")
	for i := 0; i+1 < len(want); i += 2 {
		if !containsPair(created, want[i], want[i+1]) {
			t.Fatalf("apple create lacks %s %s: %q", want[i], want[i+1], created)
		}
	}
	name, err := os.ReadFile(os.Getenv("APPLE_NAME"))
	if err != nil {
		t.Fatal(err)
	}
	if pid, ok := ownerFromName(string(name)); !ok || pid != os.Getpid() {
		t.Fatalf("apple container name %q does not name this process", name)
	}
}

func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The brief's three cases on a Docker-compatible engine, where the label filter
// is the engine's: the same host name under another machine id is never
// swept, the same id with a dead owner is, and without an id nothing is.
func TestSweepCompatOwnerIdentity(t *testing.T) {
	host, _ := os.Hostname()
	for _, c := range []struct {
		name        string
		labeled     func(machine string) string // the machine label the container carries
		wantRemoved bool
	}{
		{"same host name, another machine", func(string) string { return strings.Repeat("f", 32) }, false},
		{"same machine, dead owner", func(machine string) string { return machine }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			machine := thisMachine(t)
			dead := exitedProcess(t)
			label := ownerMachineLabel + "=" + c.labeled(machine)
			// The fake answers like an engine honoring the filter: it lists
			// the container only when its labels match the one asked for.
			calls := fakeFilteringEngine(t, "docker", label, legacyHostLabel+"="+host, sweepName(dead))
			removed := SweepOrphanContainers(context.Background(), domain.ExecutionConfig{Image: "checks:dev", Runtime: "docker"})
			if got := len(removed) == 1 && len(removals(calls())) == 1; got != c.wantRemoved {
				t.Fatalf("removed %v (calls %q), want removed=%v", removed, calls(), c.wantRemoved)
			}
		})
	}
	t.Run("no machine id", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("WARDEN_CONFIG_DIR", filepath.Join(blocker, "warden"))
		calls := fakeSweepEngine(t, "podman", sweepName(exitedProcess(t))+"\n")
		if removed := SweepOrphanContainers(context.Background(), domain.ExecutionConfig{Image: "checks:dev", Runtime: "podman"}); len(removed) != 0 || len(calls()) != 0 {
			t.Fatalf("swept without a machine id: removed %v, calls %q", removed, calls())
		}
		if labels := labelArgs(); labels != nil {
			t.Fatalf("labels without a machine id: %q", labels)
		}
	})
}

// fakeFilteringEngine is a Docker-compatible CLI holding one container, name,
// labeled with each of labels; `ps --filter label=X` lists it only when X is
// one of them.
func fakeFilteringEngine(t *testing.T, bin, label, otherLabel, name string) (calls func() []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI uses POSIX shell")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := `#!/bin/sh
echo "$*" >> ` + shellQuote(log) + `
case "$1" in
ps)
  case "$4" in
  ` + shellQuote("label="+label) + `|` + shellQuote("label="+otherLabel) + `) echo ` + shellQuote(name) + ` ;;
  esac ;;
rm) exit 0 ;;
*) exit 90 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, bin), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		data, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
}
