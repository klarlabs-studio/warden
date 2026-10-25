package steps

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// Containers left by releases before v0.38.0.
//
// Those releases named a step container `warden-<nonce>` — no owner pid in the
// name and no owner labels on it — and removed it only when the run that
// started it got as far as removing it. A crashed run left it behind, and
// SweepOrphanContainers cannot help: it removes only what it can attribute to
// a dead owner on this machine, and these containers name no owner at all.
//
// So they are removed only on an explicit request (`warden clean
// --legacy-containers`), and even then conservatively:
//
//   - the name must be exactly `warden-` and 32 lower-case hex digits, the
//     shape those releases gave, so neither a current `warden-<pid>-<nonce>`
//     container nor someone else's `warden-db` matches;
//   - a container carrying any de.klarlabs.warden.* label is a current
//     warden's and is never touched;
//   - only stopped containers are listed, and the removal is NOT forced, so
//     an engine refuses a running one even if a listing filter was ignored.

// legacyLabelPrefix marks every label a current warden puts on a container.
const legacyLabelPrefix = "de.klarlabs.warden."

// LegacyContainerResult is what RemoveLegacyContainers did on one engine.
type LegacyContainerResult struct {
	// Engine is the runtime name: docker, podman, nerdctl or apple.
	Engine string
	// Removed lists the containers removed.
	Removed []string
	// Failed lists the containers the engine refused to remove.
	Failed []string
	// Err is set when the engine could not be listed at all.
	Err error
}

// RemoveLegacyContainers removes the stopped, unlabeled pre-v0.38.0 warden
// containers on every installed engine, whatever the repository's execution
// configuration: they may have been started under a configuration that has
// since changed. An engine whose CLI is not installed is skipped silently.
func RemoveLegacyContainers(ctx context.Context) []LegacyContainerResult {
	var results []LegacyContainerResult
	for _, runtimeName := range containerRuntimes(domain.ExecutionConfig{}) {
		bin, err := exec.LookPath(runtimeExecutable(runtimeName))
		if err != nil {
			continue
		}
		call := func(args ...string) ([]byte, error) {
			callCtx, cancel := context.WithTimeout(ctx, sweepTimeout)
			defer cancel()
			return containerProbeContext(callCtx, bin, args...)
		}
		results = append(results, removeLegacy(runtimeName, call))
	}
	return results
}

// removeLegacy lists and removes one engine's legacy containers through call,
// which runs the engine's CLI. Separate from the engine lookup so the
// selection and removal can be tested without an engine.
func removeLegacy(runtimeName string, call func(args ...string) ([]byte, error)) LegacyContainerResult {
	res := LegacyContainerResult{Engine: runtimeName}
	var names []string
	remove := []string{"rm"}
	if runtimeName == "apple" {
		out, err := call("list", "--all", "--format", "json")
		if err != nil {
			res.Err = err
			return res
		}
		names = legacyAppleCandidates(out)
		remove = []string{"delete"}
	} else {
		out, err := call("ps", "--all",
			"--filter", "name="+containerNamePrefix,
			"--filter", "status=exited", "--filter", "status=created",
			"--format", "{{.Names}}\t{{.Labels}}")
		if err != nil {
			res.Err = err
			return res
		}
		names = legacyCompatCandidates(out)
	}
	for _, name := range names {
		if _, err := call(append(remove, name)...); err != nil {
			res.Failed = append(res.Failed, name)
			continue
		}
		res.Removed = append(res.Removed, name)
	}
	return res
}

// isLegacyName reports whether name has the shape releases before v0.38.0
// gave a step container: `warden-` and a 32-digit lower-case hex nonce.
func isLegacyName(name string) bool {
	nonce, ok := strings.CutPrefix(name, containerNamePrefix)
	return ok && len(nonce) == 32 && strings.Trim(nonce, "0123456789abcdef") == ""
}

// legacyCompatCandidates reads a Docker-compatible listing of
// `{{.Names}}\t{{.Labels}}` lines and returns the legacy containers in it.
// Labels render differently between engines (`k=v,k=v` or `map[k:v]`), so a
// warden label is recognized by its prefix appearing anywhere in the field,
// which errs towards leaving a container alone.
func legacyCompatCandidates(out []byte) []string {
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		name, labels, _ := strings.Cut(strings.TrimSpace(line), "\t")
		name = strings.TrimSpace(name)
		if !isLegacyName(name) || strings.Contains(labels, legacyLabelPrefix) {
			continue
		}
		names = append(names, name)
	}
	return names
}

// legacyAppleCandidates reads Apple's `list --all --format json` and returns
// the legacy containers in it. Apple's list has no status filter, so a
// container counts as stopped only when its status says so explicitly.
func legacyAppleCandidates(out []byte) []string {
	var containers []struct {
		Status        json.RawMessage `json:"status"`
		Configuration struct {
			ID     string            `json:"id"`
			Labels map[string]string `json:"labels"`
		} `json:"configuration"`
	}
	if json.Unmarshal(out, &containers) != nil {
		return nil
	}
	var names []string
	for _, c := range containers {
		if !isLegacyName(c.Configuration.ID) || !strings.EqualFold(appleState(c.Status), "stopped") || hasWardenLabel(c.Configuration.Labels) {
			continue
		}
		names = append(names, c.Configuration.ID)
	}
	return names
}

// appleState reads a container's state from Apple's status field, which
// current releases render as an object ({"state":"running",…}) and earlier
// ones as a bare string. Anything else reads as unknown, which is never
// "stopped".
func appleState(raw json.RawMessage) string {
	var state string
	if json.Unmarshal(raw, &state) == nil {
		return state
	}
	var status struct {
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &status) == nil {
		return status.State
	}
	return ""
}

func hasWardenLabel(labels map[string]string) bool {
	for k := range labels {
		if strings.HasPrefix(k, legacyLabelPrefix) {
			return true
		}
	}
	return false
}
