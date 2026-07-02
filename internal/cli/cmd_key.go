package cli

import (
	"fmt"
	"io"
)

// cmdKey handles `warden key show`, printing this machine's provenance signing
// identity: the fingerprint to pin as a trusted signer (`warden verify --key`)
// and the full public key. The private key never leaves the machine.
func cmdKey(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] != "show" {
		fmt.Fprintln(stderr, "usage: warden key show")
		return 2
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	pub, fp := svc.SigningKey()
	if pub == "" {
		fmt.Fprintln(stderr, "warden: no signing key available (config dir unwritable?)")
		return 1
	}
	fmt.Fprintf(stdout, "fingerprint: %s\n", fp)
	fmt.Fprintf(stdout, "public key:  %s\n", pub)
	fmt.Fprintln(stdout, "\nPin this in CI to require warden-signed provenance:")
	fmt.Fprintf(stdout, "  warden verify --key %s\n", fp)
	return 0
}
