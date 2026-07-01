package steps

import (
	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// Default returns the registry of Warden's built-in steps. Shell steps back
// onto configured commands; agent steps carry the instruction handed to the
// coding agent. Custom steps are not listed here — they resolve to a subprocess
// adapter at run time.
func Default() application.Registry {
	return application.Registry{
		domain.StepIntent: NewAgentStep(domain.StepIntent,
			"Summarize the intent of the staged change and flag anything that contradicts the commit message."),
		domain.StepRebase: NewRebaseStep(),
		domain.StepReview: NewAgentStep(domain.StepReview,
			"Review this change for correctness, security, and clarity bugs. Report blocking issues."),
		domain.StepTest: NewShellStep(domain.StepTest, "test"),
		domain.StepDocument: NewAgentStep(domain.StepDocument,
			"Check that public APIs and user-facing behavior changed here are documented; note gaps."),
		domain.StepLint: NewShellStep(domain.StepLint, "lint"),
	}
}

// BuiltinNames lists the built-in step names for `warden steps list`.
func BuiltinNames() []domain.StepName {
	return []domain.StepName{
		domain.StepIntent, domain.StepRebase, domain.StepReview,
		domain.StepTest, domain.StepDocument, domain.StepLint,
	}
}
