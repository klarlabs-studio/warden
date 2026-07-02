package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// cachedStep wraps a step so a run whose declared inputs are byte-identical to a
// prior passing run is skipped (returns a synthetic pass) instead of re-running
// the command. The pass still flows through the kernel, so its evidence is
// recorded and the provenance chain stays complete. Only non-mutating steps are
// wrapped (see ResolvedPolicy.Cacheable).
type cachedStep struct {
	inner   application.Step
	cache   application.StepCache
	globs   []string
	command string
}

func (c cachedStep) Name() domain.StepName { return c.inner.Name() }

func (c cachedStep) Run(ctx context.Context, sc application.StepContext) (domain.StepResult, error) {
	fp := c.cache.Fingerprint(sc.WorktreeDir, c.globs)
	var key string
	if fp != "" {
		key = cacheKey(c.inner.Name(), c.command, fp)
		if c.cache.Seen(key) {
			return domain.StepResult{
				Step:    c.inner.Name(),
				Status:  domain.StepPass,
				Summary: string(c.inner.Name()) + " (cached — inputs unchanged)",
			}, nil
		}
	}

	res, err := c.inner.Run(ctx, sc)
	// Only a clean pass is cached; a failure or a fixed/mutated run must re-run.
	if err == nil && key != "" && res.Status == domain.StepPass && !res.Fixed {
		c.cache.Record(key)
	}
	return res, err
}

// cacheKey binds the step name, its resolved command, and the input fingerprint
// so a command change or input change both miss the cache.
func cacheKey(name domain.StepName, command, fingerprint string) string {
	h := sha256.New()
	h.Write([]byte(name))
	h.Write([]byte{0})
	h.Write([]byte(command))
	h.Write([]byte{0})
	h.Write([]byte(fingerprint))
	return hex.EncodeToString(h.Sum(nil))
}
