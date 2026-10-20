package proc

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.klarlabs.de/warden/internal/infrastructure/signing"
)

// machineIDFile holds this machine's owner id, under warden's per-user config
// dir beside the signing key.
const machineIDFile = "machine-id"

// machineIDLen is the id's length in hex digits (128 random bits).
const machineIDLen = 32

// MachineID is the identifier warden records beside a pid when it leaves
// something behind for a later run to sweep: a container's owner label, a
// worktree's owner marker. A sweep removes a leftover only when the id matches
// its own and the recorded pid is dead, so the id has to say "this machine",
// not merely "a machine with this name".
//
// The host name could not say that. Two machines sharing a remote container
// engine (DOCKER_HOST on another box) commonly share a host name too — every
// unrenamed Mac is MacBook-Pro.local, DHCP hands out the same names — and to
// either of them the other's pid reads as dead, so each would remove the
// other's running containers.
//
// The id is 128 random bits generated on first use and kept in the config dir
// (WARDEN_CONFIG_DIR overrides it). ok is false when it can neither be read nor
// created, or the file holds something else; callers then record and remove
// nothing rather than guess. A malformed file is never overwritten: whatever
// was labeled with the old value would be orphaned for good.
func MachineID() (id string, ok bool) {
	dir, err := signing.DefaultDir()
	if err != nil {
		return "", false
	}
	id, err = machineIDIn(dir)
	return id, err == nil
}

// machineIDIn reads the id under dir, creating it on first use.
func machineIDIn(dir string) (string, error) {
	path := filepath.Join(dir, machineIDFile)
	id, err := readMachineID(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return id, err
	}
	if err := publishMachineID(dir, path); err != nil {
		return "", err
	}
	return readMachineID(path)
}

// publishMachineID writes a fresh id to path unless one is already there.
//
// Two processes starting on a new machine at once must end up with one id,
// not one each. The id is written in full to a temporary file and hard-linked
// into place, which fails rather than replaces when path exists: whoever
// links first wins, and everyone then reads the winner's id. A reader never
// sees a half-written file.
func publishMachineID(dir, path string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw := make([]byte, machineIDLen/2)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".machine-id-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.WriteString(hex.EncodeToString(raw) + "\n")
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}

// readMachineID reads and validates the id at path.
func readMachineID(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // warden's own config dir
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(raw))
	if len(id) != machineIDLen || strings.Trim(id, "0123456789abcdef") != "" {
		return "", fmt.Errorf("%s is not a warden machine id", path)
	}
	return id, nil
}
