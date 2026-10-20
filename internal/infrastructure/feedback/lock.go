package feedback

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/proc"
)

// LockPath is the per-worktree watcher lock.
func LockPath(gitDir string) string { return filepath.Join(Dir(gitDir), "watch.lock") }

// Holder is what a lock file records about the watcher holding it.
type Holder struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// HeldError reports that another live watcher holds the worktree's lock.
type HeldError struct{ Holder Holder }

func (e *HeldError) Error() string {
	return fmt.Sprintf("another watcher (pid %d, started %s) is writing background feedback for this worktree",
		e.Holder.PID, e.Holder.StartedAt.Local().Format(time.RFC3339))
}

// ErrLockLost means the lock file no longer names this watcher: it was judged
// stale (a suspended machine stops heartbeats) and another watcher took over.
var ErrLockLost = errors.New("the background feedback lock was taken over by another watcher")

// Lock is a held watcher lock. The lock is a file created with O_EXCL that
// names the holder's pid and start time; its modification time is the
// holder's heartbeat.
type Lock struct {
	path   string
	holder Holder
	data   []byte
}

// lockStaleAfter is how old a lock's heartbeat may be before another watcher
// treats it as abandoned, even if its pid is alive (pids are reused). A
// variable so tests need not wait it out.
var lockStaleAfter = domain.FeedbackProducerTimeout

// unreadableGrace is how long a lock file that cannot be parsed is left
// alone: a watcher that just created it may not have written it yet.
const unreadableGrace = 2 * time.Second

// Acquire takes the worktree's watcher lock for this process. A lock held by
// a live, heartbeating watcher is a *HeldError. A stale one (holder gone, or
// heartbeat older than the producer timeout) is taken over.
func Acquire(gitDir string) (*Lock, error) {
	path := LockPath(gitDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	holder := Holder{PID: os.Getpid(), StartedAt: time.Now().UTC().Truncate(time.Millisecond)}
	data, err := json.Marshal(holder)
	if err != nil {
		return nil, err
	}
	l := &Lock{path: path, holder: holder, data: data}
	for range 3 {
		created, err := createExclusive(path, data)
		if err != nil {
			return nil, err
		}
		if created {
			return l, nil
		}
		existing, stale, err := inspect(path)
		if errors.Is(err, os.ErrNotExist) {
			continue // released between our create and our read
		}
		if err != nil {
			return nil, err
		}
		if !stale {
			return nil, &HeldError{Holder: existing.holder}
		}
		if err := takeOver(path, existing); err != nil {
			return nil, err
		}
	}
	existing, _, err := inspect(path)
	if err != nil {
		return nil, fmt.Errorf("background feedback lock: %w", err)
	}
	return nil, &HeldError{Holder: existing.holder}
}

func createExclusive(path string, data []byte) (bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(path)
		return false, errors.Join(werr, cerr)
	}
	return true, nil
}

type lockState struct {
	holder Holder
	data   []byte
}

// inspect reads the lock and decides whether it is abandoned.
func inspect(path string) (lockState, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return lockState{}, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return lockState{}, false, err
	}
	age := time.Since(info.ModTime())
	var holder Holder
	if json.Unmarshal(data, &holder) != nil || holder.PID <= 0 {
		return lockState{data: data}, age > unreadableGrace, nil
	}
	state := lockState{holder: holder, data: data}
	if holder.PID == os.Getpid() {
		return state, false, nil
	}
	return state, !proc.Alive(holder.PID) || age > lockStaleAfter, nil
}

// takeOver removes an abandoned lock without removing a fresh one another
// watcher created in the meantime: the file is first moved aside, and only
// deleted if it is still the lock that was judged abandoned. Otherwise it is
// put back.
func takeOver(path string, judged lockState) error {
	aside := path + ".stale." + strconv.Itoa(os.Getpid())
	if err := os.Rename(path, aside); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := os.ReadFile(aside)
	if err == nil && bytes.Equal(data, judged.data) {
		return os.Remove(aside)
	}
	// Not the lock we judged: another watcher replaced it first. Restore it
	// unless a third has already created a new one.
	if err == nil {
		_, _ = createExclusive(path, data)
	}
	return os.Remove(aside)
}

// Holder returns what this lock records.
func (l *Lock) Holder() Holder { return l.holder }

// Heartbeat refreshes the lock's modification time after checking that it
// still names this watcher. It returns ErrLockLost when it does not.
func (l *Lock) Heartbeat() error {
	data, err := os.ReadFile(l.path)
	if err != nil || !bytes.Equal(data, l.data) {
		return ErrLockLost
	}
	now := time.Now()
	return os.Chtimes(l.path, now, now)
}

// Release removes the lock if it still names this watcher.
func (l *Lock) Release() error {
	data, err := os.ReadFile(l.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !bytes.Equal(data, l.data) {
		return nil
	}
	return os.Remove(l.path)
}
