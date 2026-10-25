package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"go.klarlabs.de/warden/internal/infrastructure/buildcache"
	"go.klarlabs.de/warden/internal/infrastructure/steps"
)

// removeLegacyContainers is the engine-facing half of --legacy-containers, a
// variable so the command's reporting can be tested without a container
// engine.
var removeLegacyContainers = steps.RemoveLegacyContainers

// cmdClean handles `warden clean [--legacy-containers]`.
//
// It removes the clone's build cache, which runs otherwise only remove once it
// outgrows its bound. A cache a running gate or watcher is using is left
// alone and the command exits with the lock-contention code, so a script can
// tell "nothing to do" from "try again later".
//
// --legacy-containers additionally removes stopped step containers left by
// releases before v0.38.0. The automatic orphan sweep cannot attribute those
// to an owner, so it never removes them; this is the explicit request it
// waits for.
func cmdClean(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	fs.SetOutput(stderr)
	legacy := fs.Bool("legacy-containers", false, "also remove stopped, unlabeled warden-<nonce> containers left by releases before v0.38.0")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "clean", "") {
		return 2
	}
	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	dir, err := svc.BuildCacheDir()
	if err != nil {
		return fail(stderr, err)
	}
	code := cleanBuildCache(dir, stdout, stderr)
	if *legacy {
		if c := cleanLegacyContainers(stdout, stderr); code == 0 {
			code = c
		}
	}
	return code
}

// cleanBuildCache removes the build cache at dir and reports what happened.
func cleanBuildCache(dir string, stdout, stderr io.Writer) int {
	ev, err := buildcache.Clean(dir)
	switch {
	case errors.Is(err, buildcache.ErrBusy):
		_, _ = fmt.Fprintf(stderr, "warden: build cache %s is in use by a running warden (a hook or watcher); nothing removed — try again when it finishes\n", dir)
		return exitContention
	case err != nil:
		return fail(stderr, fmt.Errorf("build cache %s: %w", dir, err))
	case ev.Removed:
		_, _ = fmt.Fprintf(stdout, "removed build cache %s (%s)\n", dir, buildcache.FormatSize(ev.Size))
	default:
		_, _ = fmt.Fprintf(stdout, "build cache %s is already empty\n", dir)
	}
	return 0
}

// cleanLegacyContainers removes pre-v0.38.0 containers on every installed
// engine. An engine that cannot be listed (installed but not running) is
// reported and skipped; a container an engine refused to remove fails the
// command, since the caller asked for it to be gone.
func cleanLegacyContainers(stdout, stderr io.Writer) int {
	results := removeLegacyContainers(context.Background())
	if len(results) == 0 {
		_, _ = fmt.Fprintln(stdout, "legacy containers: no container engine installed")
		return 0
	}
	code := 0
	for _, r := range results {
		switch {
		case r.Err != nil:
			_, _ = fmt.Fprintf(stderr, "warden: %s: could not list containers (%v); skipped\n", r.Engine, r.Err)
			continue
		case len(r.Removed) == 0 && len(r.Failed) == 0:
			_, _ = fmt.Fprintf(stdout, "%s: no legacy warden containers\n", r.Engine)
		}
		if len(r.Removed) > 0 {
			_, _ = fmt.Fprintf(stdout, "%s: removed %d legacy container(s): %s\n", r.Engine, len(r.Removed), strings.Join(r.Removed, ", "))
		}
		if len(r.Failed) > 0 {
			_, _ = fmt.Fprintf(stderr, "warden: %s: could not remove %s\n", r.Engine, strings.Join(r.Failed, ", "))
			code = 1
		}
	}
	return code
}
