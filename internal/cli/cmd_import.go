package cli

import (
	"flag"
	"fmt"
	"io"
	"sort"

	"gopkg.in/yaml.v3"
)

// cmdImport handles `warden import [--write]`, generating a starter .warden.yaml
// from the lint/test/security commands the repo already declares. Without
// --write it is a dry run: it prints the detected commands, the notes, and the
// YAML it would write, so the author can review before adopting. With --write it
// persists .warden.yaml.
func cmdImport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	write := fs.Bool("write", false, "write the detected config to .warden.yaml (default: dry-run, print only)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	cfg, notes, err := svc.ImportConfig(*write)
	if err != nil {
		return fail(stderr, err)
	}

	for _, note := range notes {
		fmt.Fprintf(stdout, "- %s\n", note)
	}

	if len(cfg.Commands) == 0 {
		return 0
	}

	fmt.Fprintln(stdout, "\ndetected commands:")
	for _, name := range sortedKeys(cfg.Commands) {
		fmt.Fprintf(stdout, "  %-14s %s\n", name, cfg.Commands[name])
	}

	if *write {
		fmt.Fprintln(stdout, "\nwrote .warden.yaml")
		return 0
	}

	// Dry run: show exactly what --write would persist.
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "\nwould write .warden.yaml (re-run with --write to save):\n\n%s", data)
	return 0
}

// sortedKeys returns a map's keys in sorted order for stable output.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
