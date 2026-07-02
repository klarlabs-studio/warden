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
		fmt.Fprintln(stdout, "available recipes (warden recipes <name> to see the snippet):")
		for _, r := range domain.Recipes {
			fmt.Fprintf(stdout, "  %-14s %s\n", r.Name, r.Summary)
		}
		return 0
	}

	r, ok := domain.RecipeByName(args[0])
	if !ok {
		fmt.Fprintf(stderr, "warden: no recipe %q; run `warden recipes` to list them\n", args[0])
		return 1
	}
	fmt.Fprintf(stdout, "# %s — %s\n%s\n", r.Name, r.Summary, r.Snippet)
	return 0
}
