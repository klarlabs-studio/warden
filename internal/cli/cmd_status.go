package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/git"
	"go.klarlabs.de/warden/internal/infrastructure/notify"
	"go.klarlabs.de/warden/internal/infrastructure/scanner"
)

// cmdStatus handles the bare `warden` invocation. The spec envisions a TUI that
// attaches to an in-flight pre-push run; Warden's execution model is
// synchronous (the run completes inside the hook, there is no daemon to attach
// to), so the bare command instead reports the gate's current state: which
// hooks are armed, the resolved default policy, and the adoption point. This is
// the honest analog given the architecture — a status view, not a live attach.
func cmdStatus(stdout, stderr io.Writer) int {
	svc, err := newService(autoApprover{})
	if err != nil {
		// Outside a repo (or before init) fall back to help rather than error.
		return cmdHelp(stdout)
	}

	_, _ = fmt.Fprintf(stdout, "warden %s — %s\n\n", Version, svc.Repo().Dir)

	installed, err := svc.InstalledHooks()
	if err != nil {
		return fail(stderr, err)
	}
	pins, _ := svc.HookPins() // diagnostic only; a nil map just omits the pin
	_, _ = fmt.Fprintln(stdout, "hooks:")
	for _, h := range domain.AllHooks {
		state := "not installed"
		if installed[h] {
			state = "armed"
			if v := pins[h]; v != "" {
				state += " (pinned " + v + ")"
			}
		}
		_, _ = fmt.Fprintf(stdout, "  %-11s %s\n", h, state)
	}
	if line := pinSkewLine(pins, Version); line != "" {
		_, _ = fmt.Fprintf(stdout, "\n%s\n", line)
	}

	if adoption, err := svc.Repo().ReadAdoption(); err == nil && adoption != "" {
		_, _ = fmt.Fprintf(stdout, "\nadoption point: %s\n", short(adoption))
	} else {
		_, _ = fmt.Fprintln(stdout, "\nnot initialized — run `warden init`")
	}

	_, _ = fmt.Fprintf(stdout, "\nprovenance: %s\n", provenanceMode(svc))

	pre, push, err := svc.StepsList()
	if err == nil {
		_, _ = fmt.Fprintln(stdout, "\nsteps:")
		printSteps(stdout, "  pre-commit", pre)
		printSteps(stdout, "  pre-push", push)
		if tip := watchTip(installed, pre, push); tip != "" {
			_, _ = fmt.Fprintf(stdout, "\n%s\n", tip)
		}
	}
	if line := notifyAdviceLine(svc); line != "" {
		_, _ = fmt.Fprintf(stdout, "\n%s\n", line)
	}
	if line := scannerPinLine(svc); line != "" {
		_, _ = fmt.Fprintf(stdout, "\n%s\n", line)
	}
	_, _ = fmt.Fprintln(stdout, "\nrun `warden policy explain` for the fully resolved policy.")
	return 0
}

// notifyAdviceLine warns when desktop notifications are enabled but will be
// degraded on this machine. It stays silent for a repo that turned them off —
// there is nothing to fix — and for a config it cannot read, since a diagnostic
// must never be the thing that fails `warden status`.
func notifyAdviceLine(svc interface{ Config() (domain.Config, error) }) string {
	cfg, err := svc.Config()
	if err != nil || (cfg.Notify != nil && !*cfg.Notify) {
		return ""
	}
	if advice := notify.Advice(); advice != "" {
		return "note: " + advice
	}
	return ""
}

// pinSkewLine reports hooks whose pinned version differs from the binary that
// actually runs them. The shims prefer a warden on PATH, so the pin only ever
// governs a machine with no global install — meaning a pin/binary mismatch is
// the normal case for most developers and is silently in force at every commit.
// Naming it here is the honest counterpart to that design: the pin is not a
// lock, and `warden status` should say so before a version-dependent
// disagreement has to be reverse-engineered. Pure so the wording is testable.
func pinSkewLine(pins map[domain.Hook]string, running string) string {
	var skewed []string
	for _, h := range domain.AllHooks {
		if v := pins[h]; v != "" && v != running {
			skewed = append(skewed, fmt.Sprintf("%s pins %s", h, v))
		}
	}
	if len(skewed) == 0 {
		return ""
	}
	return fmt.Sprintf("note: %s, but %s is what runs (hooks prefer a warden on PATH).\n      re-pin with `warden hooks repin`.",
		strings.Join(skewed, "; "), running)
}

// scannerPinLine reports whether the scanner version-drift check can actually
// run, and what it compares.
//
// The check refuses to scan when the local scanner differs from the version CI
// pins — but it finds that pin by searching this repo's workflows, and is
// SILENT when it finds none. Silence then means two opposite things: "checked,
// the versions agree" and "found no pin, checked nothing". A repo whose pin
// lives in a shared reusable workflow gets the second and cannot tell (#112).
//
// Status is the right place to say so: naming it on every push would be noise
// for the many repos that pin nothing, but a developer asking about the gate's
// state deserves to know which of its controls are inert. Returns "" when the
// repo runs no recognizable scanner at all — there is nothing to report then.
func scannerPinLine(svc interface {
	Config() (domain.Config, error)
	Repo() *git.Repo
}) string {
	cfg, err := svc.Config()
	if err != nil || svc.Repo() == nil {
		return ""
	}
	scan, ok := scanner.ParseCommand(cfg.Commands[string(domain.StepSecurityScan)])
	if !ok {
		return "" // no scanner warden can interpret: nothing to say
	}
	if cfg.SecurityScan.VersionCheck != nil && !*cfg.SecurityScan.VersionCheck {
		return "note: scanner version check is disabled (security_scan.version_check: false)."
	}

	root := svc.Repo().Dir
	pin, found, err := scanner.DiscoverPin(context.Background(), root, scan.Binary, cfg.SecurityScan.PinFile)
	local := scanner.LocalVersion(context.Background(), root, scan.Binary)
	switch {
	case err != nil:
		return "note: scanner version check could not read the workflows (" + err.Error() + ")."
	case !found:
		return "note: scanner version check is INERT — no " + scan.Binary + " pin found in .github/workflows.\n" +
			"      local " + scan.Binary + " is " + orUnknown(local) + "; nothing here says what CI runs, so drift\n" +
			"      cannot be detected. Point at the pin with security_scan.pin_file, or see issue #112\n" +
			"      if it lives in a shared reusable workflow."
	case local == "":
		return "note: " + scan.Binary + " is pinned to " + pin.Version + " (" + pin.Source + ") but is not on PATH."
	case !scanner.SameVersion(local, pin.Version):
		return "note: " + scan.Binary + " drift — " + pin.Source + " pins " + pin.Version + ", PATH has " + local + "."
	default:
		return ""
	}
}

func orUnknown(v string) string {
	if v == "" {
		return "not installed"
	}
	return v
}

// watchTip surfaces `warden watch` exactly where it earns its keep: a split
// policy that keeps pre-commit fast by deferring checks to pre-push leaves a gap
// between "commit is green" and "the suite is green", and watch is the tool
// built to close it. It stays silent unless pre-commit is the armed hook doing
// the deferring — with no pre-commit shim there is nothing to defer *from*, and
// with nothing deferred the tip is noise. Pure so the condition is testable.
func watchTip(installed map[domain.Hook]bool, pre, push []domain.StepName) string {
	if !installed[domain.PreCommit] {
		return ""
	}
	deferred := domain.DeferredSteps(pre, push)
	if len(deferred) == 0 {
		return ""
	}
	return fmt.Sprintf("tip: `warden watch` re-runs these on save, so deferred steps (%s) don't wait for push.",
		domain.JoinSteps(deferred))
}

// provenanceMode describes, in one line, which of warden's two provenance modes
// the repository is actually in.
//
// #212 §5 and §9: nothing surfaced this. A repository with no trusted_keys
// reports hooks armed and looks entirely healthy, while its notes mean only "a
// warden ran here" rather than "a warden I trust ran here" — a distinction that
// took reading the verify action's YAML to discover. And adding trusted_keys
// turns enforcement on immediately, which surprised even the author of the
// docs; verify starts rejecting anything the roster does not cover.
//
// Both facts are one line of output. Neither was printed.
// Takes a narrow interface rather than *service.Service, matching the other
// diagnostics in this file, so the wording can be tested without a repository.
func provenanceMode(svc interface {
	Config() (domain.Config, error)
	SigningKey() (publicKey, fingerprint string)
}) string {
	cfg, err := svc.Config()
	if err != nil {
		return "unknown (config unreadable)"
	}
	n := len(cfg.TrustedKeys)
	if n == 0 {
		signed := ""
		if pub, _ := svc.SigningKey(); pub == "" {
			signed = "; no signing key either, so notes will be unsigned"
		}
		return fmt.Sprintf(
			"unsigned — notes prove a warden ran, not whose%s\n"+
				"  add fingerprints to .warden.yaml trusted_keys: to enforce a roster (`warden key show`)",
			signed)
	}

	// Say whether OUR key is in the roster. A repository can enforce a roster
	// this machine is not on, in which case the gate here still runs but its
	// notes will be rejected by verify — worth knowing before a push, not after.
	mine := ""
	if _, fp := svc.SigningKey(); fp != "" {
		mine = "; this machine's key is not on it"
		for _, k := range cfg.TrustedKeys {
			if strings.EqualFold(strings.TrimSpace(k), fp) {
				mine = "; including this machine's"
				break
			}
		}
	}
	return fmt.Sprintf("signed — enforcing a roster of %d key(s)%s", n, mine)
}
