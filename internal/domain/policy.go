package domain

// ResolvedPolicy is the effective configuration for one hook invocation, after
// all matching rules have been stacked (§5.2). It is what the kernel layer
// translates into axi-go Action registrations and budgets (§5.4), and what
// `warden policy explain` renders.
type ResolvedPolicy struct {
	Hook Hook
	// Steps is the final ordered step list for this run.
	Steps []StepName
	// Agents maps a step to its resolved coding-agent binary. Absent = default.
	Agents map[StepName]string
	// AutoFix maps a step to its retry budget (MaxCapabilityInvocations).
	AutoFix map[StepName]int
	// RequireApproval forces an approval pause for the run.
	RequireApproval bool
	// Commands maps a shell-backed step to its command line.
	Commands map[string]string
	// Risk is the classification that drove rule matching.
	Risk Risk
	// MatchedRules names the rules that contributed, in declaration order,
	// for provenance in run notes (§9) and `policy explain`.
	MatchedRules []string
}

// AgentFor returns the resolved agent for a step, or "" if the default applies.
func (p ResolvedPolicy) AgentFor(s StepName) string {
	if p.Agents == nil {
		return ""
	}
	return p.Agents[s]
}

// AutoFixBudget returns the retry budget for a step (0 = no auto-fix retries).
func (p ResolvedPolicy) AutoFixBudget(s StepName) int {
	if p.AutoFix == nil {
		return 0
	}
	return p.AutoFix[s]
}
