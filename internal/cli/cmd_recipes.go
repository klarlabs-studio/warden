package cli

import (
	"fmt"
	"io"

	"go.klarlabs.de/warden/internal/domain"
)

// cmdRecipes handles `warden recipes [name]`: with no argument it lists the
// built-in check recipes; with a name it prints that recipe's paste-able
// .warden.yaml snippet. Recipes let a team add a check by copy-paste instead of
// remembering each tool's exact command.
func cmdRecipes(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stdout, "available recipes (warden recipes <name> to see the snippet):")
		for _, r := range domain.Recipes {
			_, _ = fmt.Fprintf(stdout, "  %-14s %s\n", r.Name, r.Summary)
		}
		return 0
	}

	r, ok := domain.RecipeByName(args[0])
	if !ok {
		_, _ = fmt.Fprintf(stderr, "warden: no recipe %q; run `warden recipes` to list them\n", args[0])
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "# %s — %s\n%s\n", r.Name, r.Summary, r.Snippet)
	return 0
}
