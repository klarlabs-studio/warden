package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"
)

// Anchor is the line of a configuration file that declares something: File is
// relative to the repository root, in slash form, and Line is 1-based.
type Anchor struct {
	File string
	Line int
}

// CommandAnchors reports, for each step named under `commands:`, the line that
// declares the command in force. It walks the same extends chain Load walks,
// child first, with the same containment, cycle and depth rules, and the first
// file to name a step wins, because a child's command overrides its base's.
//
// A command the files do not declare (a compliance profile's base layer, a
// built-in default) has no anchor. An anchor says where the command is
// configured; it is not a location warden observed a problem at.
func (r *Repository) CommandAnchors() (map[string]Anchor, error) {
	anchors := map[string]Anchor{}
	path := filepath.Join(r.root, FileName)
	var seen []string
	for {
		data, err := readConfigFile(path)
		if os.IsNotExist(err) {
			return anchors, nil
		}
		if err != nil {
			return nil, err
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parse %s: %w", displayName(r.root, path), err)
		}
		if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
			return anchors, nil
		}
		top := doc.Content[0]
		if commands := mapValue(top, "commands"); commands != nil && commands.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(commands.Content); i += 2 {
				key := commands.Content[i]
				if _, ok := anchors[key.Value]; !ok {
					anchors[key.Value] = Anchor{File: displayName(r.root, path), Line: key.Line}
				}
			}
		}
		extends := mapValue(top, "extends")
		if extends == nil || extends.Kind != yaml.ScalarNode || extends.Value == "" {
			return anchors, nil
		}
		if len(seen) >= maxExtends {
			return nil, fmt.Errorf("extends chain too deep (>%d) at %s", maxExtends, path)
		}
		seen = append(seen, filepath.Clean(path))
		next := extends.Value
		if !filepath.IsAbs(next) {
			next = filepath.Join(filepath.Dir(path), next)
		}
		next = filepath.Clean(next)
		if !within(r.root, next) {
			return nil, fmt.Errorf("extends target %s escapes repo root %s", next, r.root)
		}
		if slices.Contains(seen, next) {
			return nil, fmt.Errorf("extends cycle: %s already in the chain", next)
		}
		path = next
	}
}
