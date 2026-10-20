package steps

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// Opt-in SARIF ingestion (Config.Diagnostics, design 0005 §2).
//
// A failing shell check is reported as one finding carrying the tool's
// verbatim output: warden does not guess a file or line from prose. A check
// can instead declare a SARIF 2.1.0 file its command writes, and when the
// check FAILS, warden reads it and adds its results as located findings.
//
// What this never does:
//
//   - change a verdict. Only a failing step's file is read, so a pass stays a
//     pass with no findings and stays eligible for a staged prediction; the
//     exit status is the only verdict, and SARIF errors on a zero exit change
//     nothing.
//   - read outside the worktree. The command is repository-authored and, under
//     container execution, runs in a container that can write anything into
//     the mounted worktree — including a symlink to a host path the container
//     cannot see but warden, reading on the host, could. Every component of
//     the declared path must be a real directory or file (no symlink anywhere),
//     and the file is opened through an os.Root, so a swap between the check
//     and the open cannot escape either.
//   - report what this run did not produce. A file that existed before the
//     command ran and was not rewritten by it (a committed report, say) is
//     ignored rather than presented as this run's result.
//   - fail a step of its own accord. A missing, oversized, unreadable or
//     malformed file is a note beside the verdict.

const (
	// sarifMaxBytes caps how much of a declared file warden reads.
	sarifMaxBytes = 4 << 20
	// sarifMaxResults caps how many results become findings, so one report
	// cannot flood the run output or the feedback document.
	sarifMaxResults = 1000
	// containerWorkspace is where container execution mounts the worktree, so
	// an absolute URI a containerised tool wrote starts with it.
	containerWorkspace = "/workspace"
)

// diagnosticsStamp is the declared file's state before the command ran, so
// ingestion can tell a file this run wrote from one that was already there.
type diagnosticsStamp struct {
	info os.FileInfo
}

// stampDiagnostics records the declared file's state in root before the
// command runs. The zero stamp means "absent", or nothing declared.
func stampDiagnostics(root, rel string) diagnosticsStamp {
	if rel == "" || !domain.ValidDiagnosticsPath(rel) {
		return diagnosticsStamp{}
	}
	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return diagnosticsStamp{}
	}
	return diagnosticsStamp{info: info}
}

// unchanged reports whether after is the same file, unmodified, as the one
// stamped before the command ran.
func (s diagnosticsStamp) unchanged(after os.FileInfo) bool {
	return s.info != nil && os.SameFile(s.info, after) &&
		s.info.ModTime().Equal(after.ModTime()) && s.info.Size() == after.Size()
}

// ingestDiagnostics reads the SARIF file rel (repo-relative, slash-separated)
// from the worktree root and returns its results as findings, plus a note for
// the step summary: how many findings were read, or why the file was ignored.
// It never returns an error: whatever goes wrong is the note.
func ingestDiagnostics(root, rel string, before diagnosticsStamp) (findings []domain.Finding, note string) {
	data, err := readConfined(root, rel, before)
	if err != nil {
		return nil, "ignored " + rel + ": " + err.Error()
	}
	var log sarifLog
	if err := json.Unmarshal(data, &log); err != nil {
		return nil, "ignored " + rel + ": not valid SARIF JSON: " + err.Error()
	}
	if log.Version != "2.1.0" {
		return nil, fmt.Sprintf("ignored %s: SARIF version %q, want \"2.1.0\"", rel, log.Version)
	}
	findings, stats := log.findings(hostRoots(root))
	note = fmt.Sprintf("%d %s from %s", len(findings), plural(len(findings)), rel)
	if stats.outside > 0 {
		note += fmt.Sprintf("; %d %s outside the repository dropped", stats.outside, results(stats.outside))
	}
	if stats.capped > 0 {
		note += fmt.Sprintf("; %d more %s not shown (limit %d)", stats.capped, results(stats.capped), sarifMaxResults)
	}
	return findings, note
}

// results is plural's noun for a count of SARIF results.
func results(n int) string {
	if n == 1 {
		return "result"
	}
	return "results"
}

// beforeConfinedOpen runs between readConfined's checks and its open. Tests
// replace it to swap the file in that window.
var beforeConfinedOpen = func() {}

// readConfined reads rel from root, refusing anything that is not a regular
// file reached through real directories inside root, larger than the cap, or
// unchanged since before the command ran.
func readConfined(root, rel string, before diagnosticsStamp) ([]byte, error) {
	if !domain.ValidDiagnosticsPath(rel) {
		return nil, errors.New("not a relative path inside the repository")
	}
	elems := strings.Split(rel, "/")
	cur := root
	var leaf os.FileInfo
	for i, elem := range elems {
		cur = filepath.Join(cur, elem)
		shown := strings.Join(elems[:i+1], "/")
		info, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, errors.New("the check did not write it")
		case err != nil:
			return nil, fmt.Errorf("cannot inspect %s: %w", shown, err)
		case info.Mode()&fs.ModeSymlink != 0:
			return nil, fmt.Errorf("%s is a symlink", shown)
		case i < len(elems)-1 && !info.IsDir():
			return nil, fmt.Errorf("%s is not a directory", shown)
		case i == len(elems)-1 && !info.Mode().IsRegular():
			return nil, errors.New("not a regular file")
		}
		leaf = info
	}
	if leaf.Size() > sarifMaxBytes {
		return nil, fmt.Errorf("larger than %d MiB", sarifMaxBytes>>20)
	}
	if before.unchanged(leaf) {
		return nil, errors.New("not rewritten by this run (it predates the check)")
	}
	beforeConfinedOpen()
	confined, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("cannot open the worktree: %w", err)
	}
	defer func() { _ = confined.Close() }()
	// Non-blocking, then verified: the checks above cannot stop the file being
	// swapped before this open, and a FIFO opened for reading would block
	// until something wrote to it.
	f, err := confined.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|openNonBlocking, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot open: %w", err)
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(leaf, opened) {
		return nil, errors.New("replaced while being read")
	}
	data, err := io.ReadAll(io.LimitReader(f, sarifMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read: %w", err)
	}
	if len(data) > sarifMaxBytes {
		return nil, fmt.Errorf("larger than %d MiB", sarifMaxBytes>>20)
	}
	return data, nil
}

// hostRoots are the slash-separated prefixes an absolute URI may carry for a
// file in this worktree: the container mount and the host worktree path,
// both as given and with symlinks resolved (macOS temp dirs live under a
// symlinked /var).
func hostRoots(root string) []string {
	roots := []string{containerWorkspace + "/"}
	add := func(p string) {
		if p == "" {
			return
		}
		p = strings.TrimSuffix(filepath.ToSlash(p), "/") + "/"
		for _, have := range roots {
			if have == p {
				return
			}
		}
		roots = append(roots, p)
	}
	add(root)
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		add(resolved)
	}
	return roots
}

// The subset of SARIF 2.1.0 warden reads. Everything else is ignored.
type sarifLog struct {
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool struct {
		Driver struct {
			Rules []sarifRule `json:"rules"`
		} `json:"driver"`
	} `json:"tool"`
	OriginalURIBaseIDs map[string]sarifArtifact `json:"originalUriBaseIds"`
	Results            []sarifResult            `json:"results"`
}

type sarifRule struct {
	ID                   string `json:"id"`
	DefaultConfiguration *struct {
		Level string `json:"level"`
	} `json:"defaultConfiguration"`
}

type sarifArtifact struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId"`
}

type sarifResult struct {
	RuleID    string `json:"ruleId"`
	RuleIndex *int   `json:"ruleIndex"`
	Rule      *struct {
		ID    string `json:"id"`
		Index *int   `json:"index"`
	} `json:"rule"`
	Kind    string `json:"kind"`
	Level   string `json:"level"`
	Message struct {
		Text string `json:"text"`
	} `json:"message"`
	Locations []struct {
		PhysicalLocation *struct {
			ArtifactLocation *sarifArtifact `json:"artifactLocation"`
			Region           *struct {
				StartLine int `json:"startLine"`
			} `json:"region"`
		} `json:"physicalLocation"`
	} `json:"locations"`
	Suppressions []struct {
		Status string `json:"status"`
	} `json:"suppressions"`
}

type sarifStats struct {
	outside int
	capped  int
}

// findings converts every reportable result to a finding. A result located
// outside the repository is dropped and counted; a result with no location
// keeps none (warden does not invent one).
func (l sarifLog) findings(roots []string) ([]domain.Finding, sarifStats) {
	var out []domain.Finding
	var stats sarifStats
	for ri := range l.Runs {
		run := &l.Runs[ri]
		for i := range run.Results {
			res := &run.Results[i]
			if !res.reportable() {
				continue
			}
			f := domain.Finding{
				Severity:  sarifSeverity(res.level(run)),
				Message:   res.Message.Text,
				Rule:      res.ruleID(run),
				FromSARIF: true,
			}
			if f.Message == "" {
				f.Message = f.Rule
			}
			if file, line, located, inside := res.location(run, roots); located {
				if !inside {
					stats.outside++
					continue
				}
				f.File, f.Line = file, line
			}
			if len(out) == sarifMaxResults {
				stats.capped++
				continue
			}
			out = append(out, f)
		}
	}
	return out, stats
}

// reportable is false for a result that is not a problem: a kind other than
// fail (pass, notApplicable, informational, review, open) or one suppressed
// with a status other than rejected.
func (r *sarifResult) reportable() bool {
	if r.Kind != "" && r.Kind != "fail" {
		return false
	}
	for _, s := range r.Suppressions {
		if s.Status != "rejected" {
			return false
		}
	}
	return true
}

func (r *sarifResult) ruleID(run *sarifRun) string {
	switch {
	case r.RuleID != "":
		return r.RuleID
	case r.Rule != nil && r.Rule.ID != "":
		return r.Rule.ID
	}
	if rule, ok := r.rule(run); ok {
		return rule.ID
	}
	return ""
}

func (r *sarifResult) rule(run *sarifRun) (sarifRule, bool) {
	rules := run.Tool.Driver.Rules
	index := r.RuleIndex
	if index == nil && r.Rule != nil {
		index = r.Rule.Index
	}
	if index != nil && *index >= 0 && *index < len(rules) {
		return rules[*index], true
	}
	id := r.RuleID
	if id == "" && r.Rule != nil {
		id = r.Rule.ID
	}
	for _, rule := range rules {
		if id != "" && rule.ID == id {
			return rule, true
		}
	}
	return sarifRule{}, false
}

// level is the result's level, else its rule's default, else SARIF's default
// of warning.
func (r *sarifResult) level(run *sarifRun) string {
	if r.Level != "" {
		return r.Level
	}
	if rule, ok := r.rule(run); ok && rule.DefaultConfiguration != nil && rule.DefaultConfiguration.Level != "" {
		return rule.DefaultConfiguration.Level
	}
	return "warning"
}

func sarifSeverity(level string) domain.Severity {
	switch level {
	case "error":
		return domain.SeverityHigh
	case "warning":
		return domain.SeverityMedium
	case "note":
		return domain.SeverityLow
	default:
		return domain.SeverityInfo
	}
}

// location resolves the result's first physical location to a repo-relative
// file and line. located is false when the result names no file; inside is
// false when it names one warden cannot place inside the repository.
func (r *sarifResult) location(run *sarifRun, roots []string) (file string, line int, located, inside bool) {
	for _, loc := range r.Locations {
		pl := loc.PhysicalLocation
		if pl == nil || pl.ArtifactLocation == nil || pl.ArtifactLocation.URI == "" {
			continue
		}
		if pl.Region != nil && pl.Region.StartLine > 0 {
			line = pl.Region.StartLine
		}
		file, inside = normaliseSARIFURI(pl.ArtifactLocation.URI, baseURI(run, pl.ArtifactLocation.URIBaseID), roots)
		if !inside {
			line = 0
		}
		return file, line, true, inside
	}
	return "", 0, false, false
}

// baseURI resolves a uriBaseId through the run's originalUriBaseIds, following
// nested bases a few levels deep. An unknown id (such as a bare SRCROOT the
// tool never defined) resolves to "", leaving a relative URI relative to the
// repository root.
func baseURI(run *sarifRun, id string) string {
	var parts []string
	for depth := 0; id != "" && depth < 8; depth++ {
		base, ok := run.OriginalURIBaseIDs[id]
		if !ok {
			break
		}
		parts = append(parts, base.URI)
		id = base.URIBaseID
	}
	resolved := ""
	for i := len(parts) - 1; i >= 0; i-- {
		resolved = resolveURI(resolved, parts[i])
	}
	return resolved
}

// resolveURI resolves ref against base (RFC 3986), treating base as a
// directory.
func resolveURI(base, ref string) string {
	if base == "" {
		return ref
	}
	b, err := url.Parse(strings.TrimSuffix(base, "/") + "/")
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

// normaliseSARIFURI maps a SARIF artifact URI to a repo-relative,
// slash-separated path. An absolute path must start with the container mount
// or the host worktree; a relative one is taken relative to the repository
// root. Anything else — another host, another scheme, a path that climbs out
// of the repository — is outside.
func normaliseSARIFURI(raw, base string, roots []string) (string, bool) {
	u, err := url.Parse(resolveURI(base, raw))
	if err != nil {
		return "", false
	}
	switch u.Scheme {
	case "", "file":
	default:
		return "", false
	}
	if u.Host != "" && u.Host != "localhost" {
		return "", false
	}
	p := u.Path
	if u.Opaque != "" { // e.g. "file:relative/path"
		p = u.Opaque
	}
	// A Windows file URI carries its drive after a slash; drop the slash so
	// the path compares with the worktree root (drive letter first).
	if len(p) >= 4 && p[0] == '/' && p[2] == ':' && p[3] == '/' {
		p = p[1:]
	}
	rel := p
	if absoluteURIPath(p) {
		rel = ""
		for _, root := range roots {
			if strings.HasPrefix(p, root) {
				rel = strings.TrimPrefix(p, root)
				break
			}
		}
		if rel == "" {
			return "", false
		}
	}
	rel = path.Clean(rel)
	if !domain.ValidDiagnosticsPath(rel) {
		return "", false
	}
	return rel, true
}

func absoluteURIPath(p string) bool {
	return strings.HasPrefix(p, "/") || (len(p) >= 3 && p[1] == ':' && p[2] == '/')
}
