package cli

import (
	"context"
	"fmt"

	"go.klarlabs.de/warden/internal/domain"
	mcpserver "go.klarlabs.de/warden/internal/mcp"
)

// unavailableFacade answers every tool with the reason warden cannot operate
// where it was started, instead of the server refusing to start at all.
//
// `warden mcp serve` used to exit 1 before speaking a word of MCP when its
// working directory was not a git repository. That is a defensible thing to
// refuse — warden gates git — but it is the wrong way to refuse it over this
// transport. An MCP client launches the server as a subprocess and reads the
// protocol on stdout; a process that dies during startup surfaces to the user
// as "server exited" or "failed to connect", with the actual cause on a stderr
// stream most clients discard. The one piece of information they need is the
// piece they cannot see.
//
// The working directory is also not the user's choice here. It is whatever the
// client happened to launch with — an editor's cwd, a home directory — so
// starting outside a repository is a likely first run rather than an unusual
// mistake.
//
// Serving instead means the handshake succeeds, the client connects, the tools
// are listed, and calling any of them returns a sentence saying what is wrong
// and how to fix it. Nothing is pretended to work: every call fails, loudly and
// legibly.
type unavailableFacade struct {
	// dir is where the server was started, quoted back so the answer names the
	// directory rather than leaving the user to guess which one warden saw.
	dir string
	// cause is the underlying failure, kept for errors.Is/As rather than
	// flattened into a string.
	cause error
}

// compile-time proof this stays a complete Facade: adding a method to the
// interface breaks the build here rather than at a client's first tool call.
var _ mcpserver.Facade = (*unavailableFacade)(nil)

// err builds the message every tool returns.
//
// Wrapped so it reaches the client: the MCP dispatcher flattens a raw handler
// error to a bare "internal error" before it leaves the process, which is right
// for a failure that might leak internals and wrong for a refusal the caller is
// meant to resolve. Without this the explanation lands only in the server's own
// log, where the person who needs it never looks.
func (u *unavailableFacade) err() error {
	return mcpserver.VisibleError(fmt.Errorf(
		"warden has no repository to work in: %s is not a git repository. "+
			"Start the MCP server with its working directory inside the repository you want gated "+
			"(most clients set this per server, e.g. a \"cwd\" field alongside the command). "+
			"Underlying error: %w",
		u.dir, u.cause))
}

func (u *unavailableFacade) PolicyExplain(domain.Hook, string, []string) (domain.ResolvedPolicy, error) {
	return domain.ResolvedPolicy{}, u.err()
}

func (u *unavailableFacade) StepsList() (preCommit, prePush []domain.StepName, err error) {
	return nil, nil, u.err()
}

func (u *unavailableFacade) RunTrigger(context.Context, domain.Hook) (mcpserver.RunSummary, error) {
	return mcpserver.RunSummary{}, u.err()
}

func (u *unavailableFacade) RunTriggerStreaming(context.Context, domain.Hook, func(mcpserver.StepProgress)) (mcpserver.RunSummary, error) {
	return mcpserver.RunSummary{}, u.err()
}

func (u *unavailableFacade) Verify(string, []string) (mcpserver.ProvenanceRecord, error) {
	return mcpserver.ProvenanceRecord{}, u.err()
}

func (u *unavailableFacade) VerifyRange(string, string, mcpserver.RangeVerifyRequest) (mcpserver.RangeVerifyOutput, error) {
	return mcpserver.RangeVerifyOutput{}, u.err()
}

func (u *unavailableFacade) Doctor(string) (domain.AuditReport, error) {
	return domain.AuditReport{}, u.err()
}

func (u *unavailableFacade) Audit(string) (domain.AuditReport, error) {
	return domain.AuditReport{}, u.err()
}

func (u *unavailableFacade) Status() (mcpserver.StatusOutput, error) {
	return mcpserver.StatusOutput{}, u.err()
}
