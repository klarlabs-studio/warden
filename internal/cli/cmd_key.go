package cli

import (
	"fmt"
	"io"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/service"
)

// cmdKey handles `warden key show` (this machine's signing identity) and
// `warden key list` (the repo's committed trusted-signer roster). The private
// key never leaves the machine.
func cmdKey(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: warden key <show|list>")
		return 2
	}
	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	switch args[0] {
	case "show":
		return keyShow(svc, stdout, stderr)
	case "list":
		return keyList(svc, stdout, stderr)
	default:
		fmt.Fprintln(stderr, "usage: warden key <show|list>")
		return 2
	}
}

// keyShow prints this machine's fingerprint (to pin as a trusted signer) and
// full public key.
func keyShow(svc *service.Service, stdout, stderr io.Writer) int {
	pub, fp := svc.SigningKey()
	if pub == "" {
		fmt.Fprintln(stderr, "warden: no signing key available (config dir unwritable?)")
		return 1
	}
	fmt.Fprintf(stdout, "fingerprint: %s\n", fp)
	fmt.Fprintf(stdout, "public key:  %s\n", pub)
	fmt.Fprintln(stdout, "\nAdd this fingerprint to .warden.yaml `trusted_keys:` to make this")
	fmt.Fprintln(stdout, "machine a trusted signer for the repo's provenance gate.")
	return 0
}

// keyList prints the repo's committed trusted-signer roster (.warden.yaml
// `trusted_keys`) and flags whether this machine's key is in it, so an operator
// can see at a glance whether their pushes will pass a trusted-signed gate.
func keyList(svc *service.Service, stdout, stderr io.Writer) int {
	cfg, err := svc.Config()
	if err != nil {
		return fail(stderr, err)
	}
	if len(cfg.TrustedKeys) == 0 {
		fmt.Fprintln(stdout, "no trusted_keys in .warden.yaml — provenance is verified but not trust-pinned.")
		fmt.Fprintln(stdout, "add `trusted_keys: [<fingerprint>]` to require a trusted signer (see `warden key show`).")
		return 0
	}
	_, myFP := svc.SigningKey()
	fmt.Fprintf(stdout, "trusted signers (%d) from .warden.yaml:\n", len(cfg.TrustedKeys))
	for _, k := range cfg.TrustedKeys {
		mark := ""
		if k == myFP || domain.KeyFingerprint(k) == myFP {
			mark = "  <- this machine"
		}
		fmt.Fprintf(stdout, "  %s%s\n", k, mark)
	}
	return 0
}
