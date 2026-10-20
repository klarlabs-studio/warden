package git

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// TreeBlob is a regular file read out of a commit's tree: its repository path
// ('/'-separated on every OS, exactly as git stores it) and its committed
// content.
type TreeBlob struct {
	Path    string
	Content []byte
}

// ReadTreeBlobs returns every regular-file blob in rev's tree whose path keep
// accepts, in tree order, resolved in the repository at dir.
//
// It reads git objects, never the filesystem. Anything warden signs into a
// note about a commit — the SBOM, dependency drift — may state only what that
// commit contains, and a worktree holds more than that once a step has run:
// files a step generated and nobody committed, and tracked files a step
// rewrote. Reading the tree makes both invisible by construction.
//
// Only regular blobs (modes 100644 and 100755) are returned. A symlink entry's
// blob is its target path and a submodule entry has no blob here; reading
// either as file content would describe something else.
func ReadTreeBlobs(dir, rev string, keep func(path string) bool) ([]TreeBlob, error) {
	if rev == "" {
		return nil, fmt.Errorf("git: read tree blobs: empty revision")
	}
	type entry struct{ object, path string }
	// `ls-tree -z` records are "<mode> <type> <object><TAB><path><NUL>" with
	// the path unquoted, so any byte a file name can hold survives.
	out, err := runRawIn(dir, "ls-tree", "-r", "-z", "--full-tree", rev+"^{tree}")
	if err != nil {
		return nil, err
	}
	var entries []entry
	for _, rec := range strings.Split(out, "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			continue
		}
		if keep(p) {
			entries = append(entries, entry{object: fields[2], path: p})
		}
	}
	if len(entries) == 0 {
		return nil, nil
	}

	// One `git cat-file --batch` for all of them. Output is
	// "<object> <type> <size><LF><content><LF>" per request, in request order.
	var req strings.Builder
	for _, e := range entries {
		req.WriteString(e.object)
		req.WriteByte('\n')
	}
	cmd := gitCmd(dir, "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(req.String())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git cat-file --batch: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	r := bufio.NewReader(&stdout)
	blobs := make([]TreeBlob, 0, len(entries))
	for _, e := range entries {
		content, err := readBatchBlob(r, e.object)
		if err != nil {
			return nil, fmt.Errorf("cat-file %s: %w", e.path, err)
		}
		blobs = append(blobs, TreeBlob{Path: e.path, Content: content})
	}
	return blobs, nil
}

// readBatchBlob reads one `cat-file --batch` response for object, which must be
// a blob.
func readBatchBlob(r *bufio.Reader, object string) ([]byte, error) {
	header, err := r.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("truncated output")
	}
	fields := strings.Fields(header)
	if len(fields) != 3 || fields[0] != object || fields[1] != "blob" {
		return nil, fmt.Errorf("unexpected header %q", strings.TrimSpace(header))
	}
	size, err := strconv.Atoi(fields[2])
	if err != nil || size < 0 {
		return nil, fmt.Errorf("bad size in %q", strings.TrimSpace(header))
	}
	content := make([]byte, size)
	if _, err := io.ReadFull(r, content); err != nil {
		return nil, fmt.Errorf("short read: %w", err)
	}
	if _, err := r.Discard(1); err != nil { // the trailing LF
		return nil, fmt.Errorf("missing terminator")
	}
	return content, nil
}
