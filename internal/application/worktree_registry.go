package application

import (
	"sync"

	"go.klarlabs.de/warden/internal/domain"
)

// worktreeRegistry maps a step to the ephemeral worktree it must run in during a
// parallel batch (see runBatch), backing per-step worktree isolation. It is read
// concurrently by the kernel's step executors via StepContext.WorktreeFor and
// written only by the runner between batches, so an RWMutex keeps the reads
// race-free. An unregistered step resolves to "" — the caller then uses the
// canonical worktree.
type worktreeRegistry struct {
	mu   sync.RWMutex
	dirs map[domain.StepName]string
}

func newWorktreeRegistry() *worktreeRegistry {
	return &worktreeRegistry{dirs: map[domain.StepName]string{}}
}

// dirFor returns the worktree assigned to step, or "" if none (use the canonical).
func (r *worktreeRegistry) dirFor(step domain.StepName) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.dirs[step]
}

// set assigns step its worktree for the current batch.
func (r *worktreeRegistry) set(step domain.StepName, dir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dirs[step] = dir
}

// reset clears every assignment once a batch's worktrees are torn down.
func (r *worktreeRegistry) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.dirs)
}
