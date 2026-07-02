// Package git is a thin wrapper over the git CLI (via os/exec). Warden shells
// out to git rather than embedding a library so its behavior matches exactly
// what a developer sees at the terminal — hooks, config, worktrees, and notes
// all resolve through the same git the user already trusts.
package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// NotesRef is the ref Warden stores provenance under. A dedicated notes ref
// keeps run records out of the commit graph so they can be pushed, fetched,
// and rewritten independently of history.
const NotesRef = "refs/notes/warden"

// ErrBranchMoved reports that a branch advanced between the start of a run and
// the attempt to fast-forward it. It is the guard that prevents Warden from
// clobbering work another process committed mid-run.
var ErrBranchMoved = errors.New("branch moved during run")

// Repo is a handle to a git repository rooted at Dir.
type Repo struct {
	Dir string
}

// Open finds the repository root containing path and returns a Repo. Resolving
// to the top level up front means every later command runs from a stable
// working directory regardless of where the caller invoked Warden.
func Open(path string) (*Repo, error) {
	// Bootstrap: run from the requested path, not r.Dir, since we don't have a
	// root yet.
	cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git: %s is not a repository: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	return &Repo{Dir: strings.TrimSpace(stdout.String())}, nil
}

// gitCmd builds a git command rooted at dir. It centralizes construction so
// callers that need to stream stdin (e.g. git apply) share the same setup as
// run.
// hookEnvVars are the per-invocation git environment variables git sets for a
// hook process. Warden runs its own git subcommands (worktree add, diff, push)
// from inside those hooks; if they inherit these, git operates against the
// hook's temp index/dir instead of the repo — e.g. `git worktree add` fails
// with "index file open failed". Scrubbing them makes Warden's git behave as if
// run fresh at the terminal, which is the whole point of shelling out to git.
var hookEnvVars = []string{
	"GIT_INDEX_FILE",
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_PREFIX",
	"GIT_OBJECT_DIRECTORY",
	"GIT_COMMON_DIR",
	"GIT_INDEX_VERSION",
}

func gitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = scrubHookEnv(os.Environ())
	return cmd
}

// scrubHookEnv removes the git hook environment variables so a subcommand
// resolves the repository from cmd.Dir, not from an inherited GIT_DIR.
func scrubHookEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !isHookEnvVar(kv) {
			out = append(out, kv)
		}
	}
	return out
}

func isHookEnvVar(kv string) bool {
	for _, k := range hookEnvVars {
		if strings.HasPrefix(kv, k+"=") {
			return true
		}
	}
	return false
}

// run executes git with args in r.Dir and returns trimmed stdout. On failure it
// folds stderr into the error so callers get git's own diagnostic, not just an
// opaque exit code.
func (r *Repo) run(args ...string) (string, error) {
	return runIn(r.Dir, args...)
}

// runIn executes git with args in dir and returns trimmed stdout. It backs both
// Repo.run and worktree operations that must target a worktree directory rather
// than the repo root.
func runIn(dir string, args ...string) (string, error) {
	out, err := runRawIn(dir, args...)
	return strings.TrimSpace(out), err
}

// runRawIn is runIn without trimming. Diffs and patches must be applied
// byte-for-byte — trimming can strip a hunk's trailing blank context lines,
// corrupting the patch's line counts — so diff/patch capture uses this.
func runRawIn(dir string, args ...string) (string, error) {
	cmd := gitCmd(dir, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// ResolveSHA resolves a ref (branch, tag, HEAD, short sha) to a full commit SHA.
func (r *Repo) ResolveSHA(ref string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	return r.run("rev-parse", ref)
}

// CurrentBranch returns the checked-out branch name (or "HEAD" when detached).
func (r *Repo) CurrentBranch() (string, error) {
	return r.run("rev-parse", "--abbrev-ref", "HEAD")
}

// HeadSHA returns the full SHA of HEAD.
func (r *Repo) HeadSHA() (string, error) {
	return r.run("rev-parse", "HEAD")
}

// MergeBase returns the best common ancestor of HEAD and ref (e.g.
// "origin/main"), the anchor Warden diffs against to scope a change.
func (r *Repo) MergeBase(ref string) (string, error) {
	return r.run("merge-base", "HEAD", ref)
}

// DiffStats computes files touched, lines changed, and touched paths for the
// range base..HEAD, or the staged changes when base is empty. It parses
// --numstat so additions and deletions are summed exactly as git counts them.
func (r *Repo) DiffStats(base string) (domain.DiffStats, error) {
	var out string
	var err error
	if base == "" {
		out, err = r.run("diff", "--cached", "--numstat")
	} else {
		out, err = r.run("diff", "--numstat", base+"..HEAD")
	}
	if err != nil {
		return domain.DiffStats{}, err
	}
	return parseNumstat(out), nil
}

// StagedPaths returns the paths with staged changes. It reports the same
// DiffStats shape as DiffStats but only fills Paths and FilesTouched, since the
// caller needs the path set, not line counts.
func (r *Repo) StagedPaths() (domain.DiffStats, error) {
	out, err := r.run("diff", "--cached", "--name-only")
	if err != nil {
		return domain.DiffStats{}, err
	}
	paths := splitLines(out)
	return domain.DiffStats{FilesTouched: len(paths), Paths: paths}, nil
}

// parseNumstat turns `git diff --numstat` output into DiffStats. Each line is
// tab-separated as "added<TAB>deleted<TAB>path", so splitting on tabs preserves
// spaces in the path and takes the path column directly — no fragile index math
// that could slice on a not-found substring. Binary files report "-" for both
// columns; they count toward files touched but not lines.
func parseNumstat(out string) domain.DiffStats {
	var stats domain.DiffStats
	for _, line := range splitLines(out) {
		cols := strings.SplitN(line, "\t", 3)
		if len(cols) < 3 {
			continue
		}
		stats.FilesTouched++
		stats.Paths = append(stats.Paths, strings.TrimSpace(cols[2]))
		if added, err := strconv.Atoi(cols[0]); err == nil {
			stats.LinesChanged += added
		}
		if deleted, err := strconv.Atoi(cols[1]); err == nil {
			stats.LinesChanged += deleted
		}
	}
	return stats
}

// splitLines splits on newlines and drops empty entries so empty git output
// yields a nil slice rather than a phantom one-element slice.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			out = append(out, line)
		}
	}
	return out
}
