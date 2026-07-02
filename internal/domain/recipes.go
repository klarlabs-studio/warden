package domain

// Recipe is a shareable custom-step snippet: a named security/quality check a
// repo can paste into .warden.yaml. Recipes are how a team adds a check by
// copy-paste instead of remembering the exact command and wiring (§ adoption).
type Recipe struct {
	Name    string
	Summary string
	// Snippet is the .warden.yaml commands fragment to merge; the step name is
	// then added to the repo's steps.pre_push list.
	Snippet string
}

// Recipes is warden's built-in recipe registry: common checks that slot in as
// custom command steps. Kept small and dependency-honest — each names the tool
// the command needs on PATH.
var Recipes = []Recipe{
	{
		Name:    "gitleaks",
		Summary: "scan for committed secrets (needs: gitleaks)",
		Snippet: `commands:
  secrets: "gitleaks detect --no-banner --redact"
# then add "secrets" to steps.pre_push`,
	},
	{
		Name:    "semgrep",
		Summary: "static analysis / SAST over the tree (needs: semgrep)",
		Snippet: `commands:
  sast: "semgrep --error --quiet --config auto"
# then add "sast" to steps.pre_push`,
	},
	{
		Name:    "trivy",
		Summary: "filesystem vulnerability + misconfig scan (needs: trivy)",
		Snippet: `commands:
  vuln-scan: "trivy fs --exit-code 1 --quiet ."
# then add "vuln-scan" to steps.pre_push`,
	},
	{
		Name:    "coverage-delta",
		Summary: "fail if coverage drops on the change (needs: coverctl)",
		Snippet: `commands:
  coverage: "coverctl check"
# then add "coverage" to steps.pre_push`,
	},
	{
		Name:    "govulncheck",
		Summary: "reachable Go stdlib + dependency vulnerabilities (needs: govulncheck)",
		Snippet: `commands:
  vuln: "govulncheck ./..."
# then add "vuln" to steps.pre_push`,
	},
	{
		Name:    "hadolint",
		Summary: "lint Dockerfiles (needs: hadolint)",
		Snippet: `commands:
  dockerfile: "hadolint Dockerfile"
# then add "dockerfile" to steps.pre_push`,
	},
}

// RecipeByName returns the recipe with the given name, or (Recipe{}, false).
func RecipeByName(name string) (Recipe, bool) {
	for _, r := range Recipes {
		if r.Name == name {
			return r, true
		}
	}
	return Recipe{}, false
}
