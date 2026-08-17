package cli

import (
	"fmt"
	"io"

	"go.klarlabs.de/warden/internal/domain"
)

// cmdHooks handles `warden hooks enable|disable <hook>`.
func cmdHooks(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "repin" {
		return hooksRepin(stdout, stderr)
	}
	if len(args) != 2 {
		_, _ = fmt.Fprintln(stderr, "usage: warden hooks enable|disable <pre-commit|pre-push>\n       warden hooks repin")
		return 2
	}
	action := args[0]
	hook, err := domain.ParseHook(args[1])
	if err != nil {
		return fail(stderr, err)
	}

	var enabled bool
	switch action {
	case "enable":
		enabled = true
	case "disable":
		enabled = false
	default:
		_, _ = fmt.Fprintf(stderr, "warden: unknown action %q (want enable, disable or repin)\n", action)
		return 2
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	if err := svc.SetHook(hook, enabled); err != nil {
		return fail(stderr, err)
	}
	_, _ = fmt.Fprintf(stdout, "warden: %s %sd\n", hook, action)
	return 0
}

// repinTargets decides which hooks `warden hooks repin` should rewrite: those
// that are ARMED and whose recorded pin is not the running version.
//
// It is separate from the IO so the one invariant that matters can be tested
// without a repository: repin never arms anything. Repinning is about the
// version a shim records, never about which hooks run, and silently arming a
// hook someone deliberately disabled would be a worse surprise than the
// confusing verb this replaces.
func repinTargets(installed map[domain.Hook]bool, pins map[domain.Hook]string, running string) []domain.Hook {
	var out []domain.Hook
	for _, h := range domain.AllHooks {
		if installed[h] && pins[h] != running {
			out = append(out, h)
		}
	}
	return out
}

// autoRepinTargets decides which armed hooks a run should re-pin on its own:
// those whose recorded pin is strictly OLDER than the running binary.
//
// Separate from repinTargets, and deliberately narrower. `warden hooks repin`
// is someone asking for the shim to match whatever they are running, in either
// direction. This is warden deciding for them, and it only moves forward.
//
// Forward is a no-op in practice: a warden on PATH already runs whatever its
// version, so the shim's number was simply stale and the drift notice was the
// only consequence. Backward would be a real change -- a checkout with no
// warden on PATH downloads exactly the pinned version, so silently pinning
// backwards would turn one developer running an old binary into a repository
// that fetches an old binary for everyone.
func autoRepinTargets(installed map[domain.Hook]bool, pins map[domain.Hook]string, running string) []domain.Hook {
	var out []domain.Hook
	for _, h := range domain.AllHooks {
		if installed[h] && domain.IsNewer(running, pins[h]) {
			out = append(out, h)
		}
	}
	return out
}

// autoRepin brings armed hook shims up to the running version, so an upgrade
// does not leave every hook reporting drift until someone runs `warden hooks
// repin` by hand.
//
// Safe to call from inside a hook. The shim's last line is `exec warden run`,
// which replaces the shell process, so by the time this executes no shell is
// still reading the file being rewritten.
//
// Best-effort and quiet on failure: a repository where the shim cannot be
// rewritten is one that keeps working with a stale pin, which is the situation
// this improves rather than a reason to fail a gate.
func autoRepin(stdout io.Writer) {
	svc, err := newService(autoApprover{})
	if err != nil {
		return
	}
	installed, err := svc.InstalledHooks()
	if err != nil {
		return
	}
	pins, err := svc.HookPins()
	if err != nil {
		return
	}
	for _, h := range autoRepinTargets(installed, pins, Version) {
		if err := svc.RepinHook(h); err != nil {
			continue // a shim that cannot be rewritten keeps its stale pin
		}
		was := pins[h]
		if was == "" {
			was = "unpinned"
		}
		_, _ = fmt.Fprintf(stdout, "warden: repinned %s %s -> %s\n", h, was, Version)
	}
}

// hooksRepin rewrites every armed hook's shim so its pinned version matches the
// binary that is actually running.
//
// #212 §6: status already reported the drift and named `warden hooks enable
// <hook>` as the fix, which is accurate and reads wrong — "enable" describes
// arming, so the remedy for a pin looks like it would change whether the hook
// runs at all. The operation people want has its own name now.
func hooksRepin(stdout, stderr io.Writer) int {
	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	installed, err := svc.InstalledHooks()
	if err != nil {
		return fail(stderr, err)
	}
	pins, _ := svc.HookPins() // diagnostic only; an unreadable pin just repins it

	targets := repinTargets(installed, pins, Version)
	for _, h := range targets {
		if err := svc.RepinHook(h); err != nil {
			return fail(stderr, err)
		}
		was := pins[h]
		if was == "" {
			was = "unpinned"
		}
		_, _ = fmt.Fprintf(stdout, "warden: repinned %s %s -> %s\n", h, was, Version)
	}
	if len(targets) > 0 {
		return 0
	}

	if !anyInstalled(installed) {
		_, _ = fmt.Fprintln(stdout, "warden: no hooks are armed; nothing to repin (`warden hooks enable pre-commit`)")
	} else {
		_, _ = fmt.Fprintf(stdout, "warden: hooks already pin %s\n", Version)
	}
	return 0
}

func anyInstalled(installed map[domain.Hook]bool) bool {
	for _, h := range domain.AllHooks {
		if installed[h] {
			return true
		}
	}
	return false
}
