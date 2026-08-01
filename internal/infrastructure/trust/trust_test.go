package trust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Nothing is trusted until it is granted. This is the property the whole package
// exists for: an agent surface asking "may I run this repo's shell?" gets no by
// default, whatever else is true of the machine.
func TestTrusted_RefusesByDefault(t *testing.T) {
	s := New(t.TempDir())
	if s.Trusted(t.TempDir()) {
		t.Error("an ungranted repo must not be trusted")
	}
}

func TestAddThenTrusted(t *testing.T) {
	s := New(t.TempDir())
	repo := t.TempDir()

	if _, err := s.Add(repo); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !s.Trusted(repo) {
		t.Error("the repo must be trusted after Add")
	}
	// A grant names ONE subject. Trusting one repo must not trust another — the
	// whole defect this replaces was a grant that leaked across checkouts.
	if s.Trusted(t.TempDir()) {
		t.Error("trusting one repo must not trust another")
	}
}

func TestRemoveRevokes(t *testing.T) {
	s := New(t.TempDir())
	repo := t.TempDir()
	if _, err := s.Add(repo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(repo); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if s.Trusted(repo) {
		t.Error("the repo must not be trusted after Remove")
	}
}

// Add is idempotent and Remove tolerates an absent entry: both express an
// intended END STATE, and reaching it twice is not an error.
func TestAddAndRemoveAreIdempotent(t *testing.T) {
	s := New(t.TempDir())
	repo := t.TempDir()

	for range 3 {
		if _, err := s.Add(repo); err != nil {
			t.Fatalf("repeated Add: %v", err)
		}
	}
	entries, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("repeated Add produced %d entries, want 1: %v", len(entries), entries)
	}

	if _, err := s.Remove(t.TempDir()); err != nil {
		t.Errorf("removing an untrusted path must not error: %v", err)
	}
}

// A relative path, a trailing separator and the absolute path all name the same
// repository, so they must resolve to ONE entry. Otherwise the same repo could
// be trusted under several spellings and revoking one would leave the others
// still granting shell execution.
func TestCanonicalisation_OneRepoIsOneEntry(t *testing.T) {
	s := New(t.TempDir())
	repo := t.TempDir()

	if _, err := s.Add(repo); err != nil {
		t.Fatal(err)
	}
	if !s.Trusted(repo + string(filepath.Separator)) {
		t.Error("a trailing separator must resolve to the same grant")
	}

	// A relative path from inside the repo names the same subject.
	t.Chdir(repo)
	if !s.Trusted(".") {
		t.Error("a relative path must resolve to the same grant")
	}

	if _, err := s.Add(repo + string(filepath.Separator)); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the same repo under two spellings produced %d entries: %v", len(entries), entries)
	}
}

// The grant must follow the real directory, not a symlink pointing at it, or a
// symlink planted at a trusted path could redirect the grant to another tree.
func TestCanonicalisation_ResolvesSymlinks(t *testing.T) {
	s := New(t.TempDir())
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := s.Add(link); err != nil {
		t.Fatal(err)
	}
	if !s.Trusted(real) {
		t.Error("a grant made through a symlink must apply to the real directory")
	}
}

// The allowlist is the file naming every repo an agent may execute code from, so
// it must not be world-readable — same posture as the signing key beside it.
func TestFilePermissionsAreRestrictive(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if _, err := s.Add(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("trust list mode = %04o, want 0600", perm)
	}
}

// The file stays hand-editable: an operator has to be able to read and audit
// what they granted, so comments and blank lines are tolerated.
func TestList_IgnoresCommentsAndBlankLines(t *testing.T) {
	dir := t.TempDir()
	repo := t.TempDir()
	content := "# a comment\n\n" + repo + "\n\n   \n"
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(dir)
	entries, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0] != repo {
		t.Errorf("entries = %v, want just the repo path", entries)
	}
	if !s.Trusted(repo) {
		t.Error("a hand-written entry must be honored")
	}
}

// A revoke must drop EVERY spelling of the repo. Leaving a differently-spelled
// entry behind would keep granting shell execution while reporting that trust
// was revoked — the worst outcome available for this particular file.
func TestRemove_DropsEverySpelling(t *testing.T) {
	dir := t.TempDir()
	repo := t.TempDir()

	// Seed the file by hand with a non-canonical spelling, as an operator
	// editing it themselves would.
	seeded := repo + string(filepath.Separator)
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(seeded+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(dir)
	if !s.Trusted(repo) {
		t.Fatal("precondition: the hand-written entry should grant trust")
	}

	if _, err := s.Remove(repo); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if s.Trusted(repo) {
		t.Error("revoke left a differently-spelled entry still granting trust")
	}
	entries, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("entries after revoke = %v, want none", entries)
	}
}

// Adding a repo already present under another spelling must not create a second
// entry — two entries for one repo is how a revoke ends up incomplete.
func TestAdd_DoesNotDuplicateAnExistingSpelling(t *testing.T) {
	dir := t.TempDir()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(repo+string(filepath.Separator)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(dir)
	if _, err := s.Add(repo); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("entries = %v, want one entry for one repo", entries)
	}
}

// A trust check that cannot read its own policy has not established trust. The
// safe reading of "I don't know" is "no".
func TestTrusted_FailsClosed(t *testing.T) {
	s := New(t.TempDir())
	if s.Trusted("") {
		t.Error("an empty path must never be trusted")
	}

	// An unreadable allowlist must deny, not admit.
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte("/some/repo\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(path, 0o600) }()
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 000 is still readable")
	}
	if New(dir).Trusted("/some/repo") {
		t.Error("an unreadable allowlist must fail closed")
	}
}

// The written file explains itself: it authorizes shell execution, so someone
// finding it later must be able to tell what it does without the docs.
func TestWrittenFileIsSelfDescribing(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if _, err := s.Add(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"warden trust", "execute"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the file header should mention %q:\n%s", want, data)
		}
	}
}
