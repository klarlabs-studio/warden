package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A fleet that follows the advice to pin its scanner ONCE — centrally, in a
// shared reusable workflow — puts the pin where a search of this repo's
// workflows can never reach it. The check then no-ops for exactly the repos
// that did the right thing, warden's own included (#112).
//
// The fix keeps #88's principle intact: the repo says WHERE the pin lives, not
// what it is. There is still one copy of the version number, still in the
// workflow that already defines it — `security_scan.pin_file` just gains the
// ability to point across a repository boundary:
//
//	security_scan:
//	  pin_file: klarlabs-studio/.github/.github/workflows/go-ci.yml@main
//
// Resolution is deliberately unreliable-by-design: short timeout, cached, and
// any failure degrades to "no pin found", which `warden status` already reports
// as an inert check. A pre-push gate must not block on a network round trip,
// and must never fail a push because GitHub was briefly unreachable.

// remoteSpecRe matches `owner/repo/path/to/file.yml@ref`. The path must contain
// a slash and end in a YAML extension, so an ordinary repo-relative path like
// `.github/workflows/ci.yml` cannot be mistaken for a remote one (it has no
// `@ref`, which is the discriminator).
var remoteSpecRe = regexp.MustCompile(`^([^/@\s]+)/([^/@\s]+)/(.+\.ya?ml)@([^@\s]+)$`)

// RemoteSpec is a pin_file pointing at a workflow in another repository.
type RemoteSpec struct {
	Owner, Repo, Path, Ref string
}

// String renders the spec as written in config, for error messages.
func (r RemoteSpec) String() string {
	return fmt.Sprintf("%s/%s/%s@%s", r.Owner, r.Repo, r.Path, r.Ref)
}

// rawURL is where the file's bytes live. raw.githubusercontent.com serves
// public repos without authentication, which is the case this exists for: a
// shared workflow the whole fleet already reads.
func (r RemoteSpec) rawURL() string {
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", r.Owner, r.Repo, r.Ref, r.Path)
}

// ParseRemoteSpec reports whether pinFile names a workflow in another repo.
func ParseRemoteSpec(pinFile string) (RemoteSpec, bool) {
	m := remoteSpecRe.FindStringSubmatch(strings.TrimSpace(pinFile))
	if m == nil {
		return RemoteSpec{}, false
	}
	return RemoteSpec{Owner: m[1], Repo: m[2], Path: m[3], Ref: m[4]}, true
}

// remoteTimeout bounds the fetch. A gate that stalls on a slow network is a
// gate people disable, so this is short enough to be invisible on the fast path
// and is skipped entirely on a cache hit.
const remoteTimeout = 3 * time.Second

// remoteTTL is how long a resolved pin is trusted without re-fetching. A pin
// changes when someone bumps the shared workflow — days apart, not minutes — so
// an hour keeps pre-push local almost always while still noticing a bump the
// same day.
const remoteTTL = time.Hour

// httpGet is a package var so tests can serve bytes without a network.
var httpGet = func(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxWorkflowBytes))
}

// cachedPin is what gets persisted between runs.
type cachedPin struct {
	Version string    `json:"version"`
	Key     string    `json:"key"`
	At      time.Time `json:"at"`
}

// cachePath is a per-user file keyed by the spec, so two repos pointing at the
// same shared workflow share one lookup and neither writes into a git dir.
func cachePath(spec RemoteSpec) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(spec.String()))
	return filepath.Join(dir, "warden", "scanner-pin-"+hex.EncodeToString(sum[:8])+".json"), nil
}

func readCache(spec RemoteSpec) (cachedPin, bool) {
	path, err := cachePath(spec)
	if err != nil {
		return cachedPin{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cachedPin{}, false
	}
	var c cachedPin
	if err := json.Unmarshal(data, &c); err != nil || c.Version == "" {
		return cachedPin{}, false
	}
	if time.Since(c.At) > remoteTTL {
		return cachedPin{}, false
	}
	return c, true
}

func writeCache(spec RemoteSpec, c cachedPin) {
	path, err := cachePath(spec)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600) // best effort: a cache miss costs a fetch
}

// discoverRemotePin resolves a pin from another repository's workflow.
//
// Every failure path returns (Pin{}, false, nil) — not an error. An unreachable
// network, a moved file, a workflow with no pin: none of these are the
// developer's change being wrong, and none may fail a push. The check simply
// goes quiet, and `warden status` reports it as inert.
func discoverRemotePin(ctx context.Context, spec RemoteSpec, binary string) (Pin, bool, error) {
	source := spec.String()
	if c, ok := readCache(spec); ok {
		return Pin{Version: c.Version, Source: source, Key: c.Key}, true, nil
	}

	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()
	data, err := httpGet(ctx, spec.rawURL())
	if err != nil {
		return Pin{}, false, nil
	}
	pin, ok := pinFromWorkflow(data, binary, source)
	if !ok {
		return Pin{}, false, nil
	}
	writeCache(spec, cachedPin{Version: pin.Version, Key: pin.Key, At: time.Now()})
	return pin, true, nil
}
