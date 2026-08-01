package cli

import (
	"flag"
	"fmt"
	"io"
)

// rejectExtraArgs reports whether a command was handed positional arguments it
// does not take, printing a message that names the flag the caller most likely
// meant.
//
// Every flag-parsing command in this package discarded fs.Args() entirely, so
// `warden verify <sha>` parsed cleanly, verified HEAD instead, and printed
//
//	validated a80e2707237a (…, chain-intact, signed by trusted …)
//
// for a commit the caller never named. That is worse than an error: the answer
// looks authoritative, it is about a different commit, and nothing in the output
// says so. It was found by noticing the SHA in the reply did not match the SHA
// in the request — which is exactly the kind of thing a reader does not check.
//
// cmd_attest already refuses an unknown --predicate on this reasoning ("a caller
// who asked for a shape and got a different one would ship the wrong statement
// into their supply chain without ever being told"). This extends the same rule
// from flag VALUES to arguments.
//
// `fleet status` genuinely takes positional paths and must not call this.
func rejectExtraArgs(fs *flag.FlagSet, stderr io.Writer, cmd, suggest string) bool {
	if fs.NArg() == 0 {
		return false
	}
	extra := fs.Arg(0)
	if suggest != "" {
		// Naming the flag turns a refusal into a fix. The mistake is almost always
		// "I typed the value where the flag goes", not "I misunderstood the command".
		_, _ = fmt.Fprintf(stderr, "warden: %s takes no positional arguments; did you mean `--%s %s`?\n", cmd, suggest, extra)
		return true
	}
	_, _ = fmt.Fprintf(stderr, "warden: %s takes no positional arguments (got %q)\n", cmd, extra)
	return true
}
