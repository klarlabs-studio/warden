package steps

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/proc"
)

// Labels every container warden starts carries, naming the process that
// started it and the machine it ran on. A crashed run (SIGKILL, a CI
// timeout) never removes its container: Docker's --rm only fires once the
// command exits, and Apple's removal is an explicit step the killed process
// never reaches. These labels let a later run recognize such a container as
// warden's and its owner as gone.
//
// The machine is proc.MachineID, not the host name. Earlier releases labeled
// containers de.klarlabs.warden.host=<host name>, and on a container engine
// shared between machines with the same name (DOCKER_HOST on another box;
// every unrenamed Mac is MacBook-Pro.local) each machine read the other's
// live pids as dead and removed its running containers. A container carrying
// only that old label is never swept: it cannot be placed on a machine. Nor
// is the old label written any more, so an older warden on a namesake machine
// cannot match, and sweep, a container started by this one.
const (
	ownerPIDLabel     = "de.klarlabs.warden.pid"
	ownerMachineLabel = "de.klarlabs.warden.machine"
)

// containerNamePrefix starts every container name warden gives. The owner's
// pid follows it, so a Docker-compatible listing (whose label templates
// differ between engines) yields the owner from the name alone.
const containerNamePrefix = "warden-"

// containerName is the name of a container this process starts.
func containerName(nonce string) string {
	return containerNamePrefix + strconv.Itoa(os.Getpid()) + "-" + nonce
}

// ownerLabels are the label values for a container this process starts, or
// nil when the machine id is unavailable: a container that cannot be placed on
// a machine is one no sweep ever removes.
func ownerLabels() []string {
	machine, ok := proc.MachineID()
	if !ok {
		return nil
	}
	return []string{ownerPIDLabel + "=" + strconv.Itoa(os.Getpid()), ownerMachineLabel + "=" + machine}
}

// labelArgs renders ownerLabels as `--label` arguments, which Docker, Podman,
// nerdctl and Apple's CLI all accept.
func labelArgs() []string {
	var args []string
	for _, label := range ownerLabels() {
		args = append(args, "--label", label)
	}
	return args
}

// sweepTimeout bounds each engine call the sweep makes.
const sweepTimeout = 15 * time.Second

// swept records the engine CLIs this process has already swept, so a watcher
// and the gate runs inside it list each engine once.
var swept = struct {
	sync.Mutex
	bins map[string]bool
}{bins: map[string]bool{}}

// claimSweep reports whether bin still needs sweeping in this process, and
// marks it swept.
func claimSweep(bin string) bool {
	swept.Lock()
	defer swept.Unlock()
	if swept.bins[bin] {
		return false
	}
	swept.bins[bin] = true
	return true
}

// SweepOrphanContainers removes the containers warden started on this
// machine whose owning process has exited, on every installed engine the
// configuration may select. It returns the names it removed.
//
// It removes nothing it cannot positively attribute: a container without
// warden's labels, one labeled by another machine (or only with the host
// name earlier releases used), one whose owner is still
// running (a concurrent hook, another watcher) or whose owner cannot be read
// is left alone. A recycled pid makes a dead owner look alive, which leaves
// its container for a later sweep; it never makes a live owner look dead.
// Best-effort throughout: an engine that is down or answers in an unexpected
// shape is skipped, and without a machine id nothing is swept at all.
func SweepOrphanContainers(ctx context.Context, cfg domain.ExecutionConfig) []string {
	if cfg.Validate() != nil || !cfg.ContainerEnabled() {
		return nil
	}
	var removed []string
	for _, runtimeName := range containerRuntimes(cfg) {
		bin, err := exec.LookPath(runtimeExecutable(runtimeName))
		if err != nil || !claimSweep(bin) {
			continue
		}
		removed = append(removed, sweepEngine(ctx, runtimeName, bin)...)
	}
	return removed
}

// SweepSelectedInBackground sweeps the engine a run selected, once per
// process, without waiting: a gate run should not wait on an engine listing
// (most of a second on a loaded machine), and nothing it does depends on the
// result. A process that exits first simply leaves the sweep to the next run.
func SweepSelectedInBackground(selected *application.ContainerExecution) {
	if selected == nil || selected.Binary == "" || !claimSweep(selected.Binary) {
		return
	}
	go sweepEngine(context.Background(), selected.Environment.Runtime, selected.Binary)
}

// sweepEngine removes one engine's orphaned warden containers.
func sweepEngine(ctx context.Context, runtimeName, bin string) []string {
	machine, ok := proc.MachineID()
	if !ok {
		return nil
	}
	call := func(args ...string) ([]byte, error) {
		callCtx, cancel := context.WithTimeout(ctx, sweepTimeout)
		defer cancel()
		return containerProbeContext(callCtx, bin, args...)
	}
	var candidates []orphanCandidate
	if runtimeName == "apple" {
		out, err := call("list", "--all", "--format", "json")
		if err != nil {
			return nil
		}
		candidates = appleCandidates(out, machine)
	} else {
		out, err := call("ps", "--all", "--filter", "label="+ownerMachineLabel+"="+machine, "--format", "{{.Names}}")
		if err != nil {
			return nil
		}
		candidates = compatCandidates(out)
	}
	remove := []string{"rm", "--force"}
	if runtimeName == "apple" {
		remove = []string{"delete", "--force"}
	}
	var removed []string
	for _, c := range candidates {
		if c.pid == os.Getpid() || proc.Alive(c.pid) {
			continue
		}
		if _, err := call(append(remove, c.name)...); err == nil {
			removed = append(removed, c.name)
		}
	}
	return removed
}

// orphanCandidate is a container warden started on this machine, and the pid
// of the process that started it.
type orphanCandidate struct {
	name string
	pid  int
}

// compatCandidates reads a Docker-compatible listing, already filtered to
// this machine's label, one name per line. Only names of warden's own shape
// (`warden-<pid>-<nonce>`) yield an owner.
func compatCandidates(out []byte) []orphanCandidate {
	var candidates []orphanCandidate
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if pid, ok := ownerFromName(name); ok {
			candidates = append(candidates, orphanCandidate{name: name, pid: pid})
		}
	}
	return candidates
}

// ownerFromName is the owner pid encoded in a container name warden gave.
func ownerFromName(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, containerNamePrefix)
	if !ok {
		return 0, false
	}
	pidText, nonce, ok := strings.Cut(rest, "-")
	if !ok || len(nonce) != 32 || strings.Trim(nonce, "0123456789abcdef") != "" {
		return 0, false
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 || strconv.Itoa(pid) != pidText {
		return 0, false
	}
	return pid, true
}

// appleCandidates reads Apple's `list --all --format json`. Apple's CLI has
// no label filter, so the machine label is checked here, and the owner comes
// from the pid label, which must agree with the name.
func appleCandidates(out []byte, machine string) []orphanCandidate {
	var containers []struct {
		Configuration struct {
			ID     string            `json:"id"`
			Labels map[string]string `json:"labels"`
		} `json:"configuration"`
	}
	if json.Unmarshal(out, &containers) != nil {
		return nil
	}
	var candidates []orphanCandidate
	for _, c := range containers {
		labels := c.Configuration.Labels
		if labels[ownerMachineLabel] != machine {
			continue
		}
		pid, ok := ownerFromName(c.Configuration.ID)
		if !ok || labels[ownerPIDLabel] != strconv.Itoa(pid) {
			continue
		}
		candidates = append(candidates, orphanCandidate{name: c.Configuration.ID, pid: pid})
	}
	return candidates
}
