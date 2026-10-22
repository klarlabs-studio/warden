package domain

import (
	"fmt"
	"regexp"
	"slices"
)

// CIJobPolicy is protected consumer policy. A nil requirements entry means
// always run; a reusable job must name at least one exact execution contract.
type CIJobPolicy struct {
	Version int                               `json:"version"`
	Jobs    map[string]*ExecutionRequirements `json:"jobs"`
}

var ciJobName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func (p CIJobPolicy) Validate() error {
	if p.Version != 1 || len(p.Jobs) == 0 {
		return fmt.Errorf("CI job policy must use version 1 and name at least one job")
	}
	for _, name := range p.OrderedJobs() {
		r := p.Jobs[name]
		if !ciJobName.MatchString(name) {
			return fmt.Errorf("invalid CI job name %q", name)
		}
		if r == nil {
			continue
		}
		if len(r.Steps) == 0 {
			return fmt.Errorf("CI job %s requires at least one execution contract", name)
		}
		if err := r.Validate(); err != nil {
			return fmt.Errorf("CI job %s: %w", name, err)
		}
	}
	return nil
}

func (p CIJobPolicy) OrderedJobs() []string {
	names := make([]string, 0, len(p.Jobs))
	for name := range p.Jobs {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
