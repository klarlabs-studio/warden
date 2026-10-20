package watch

import (
	"context"
	"sync"
	"time"
)

// DefaultSampleTTL is how long a Sampler reuses an observation. It bounds how
// late a retagged image or a stopped engine is noticed, and it is the whole
// cost of watching the environment: one `image inspect` per TTL, rather than
// one per poll (every 400 ms) or a full runtime resolution with its engine
// `info` / `system status` probes. Nothing cheaper reveals a retag: the tag
// lives in the engine's store, which warden cannot stat.
const DefaultSampleTTL = 15 * time.Second

// Sampler caches a slowly changing observation that is too expensive to take
// on every poll, such as the container environment checks would run in. The
// value is resampled when its key changes (the configuration asks for a
// different environment) or when TTL elapses.
type Sampler struct {
	TTL time.Duration
	// Sample takes the observation. An error means the attempt was
	// inconclusive (it timed out, or the watcher is stopping), not that the
	// observed thing is absent: the previous value is kept, so one slow
	// answer under load does not read as a change and cancel the running
	// evaluation. An absence the caller could establish is a value of its own.
	Sample func(context.Context) (string, error)

	now func() time.Time // tests

	mu      sync.Mutex
	key     string
	value   string
	at      time.Time
	sampled bool
}

// Value returns the observation for key, resampling when needed.
func (s *Sampler) Value(ctx context.Context, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	ttl := s.TTL
	if ttl <= 0 {
		ttl = DefaultSampleTTL
	}
	if s.sampled && key == s.key && now().Sub(s.at) < ttl {
		return s.value
	}
	value, err := s.Sample(ctx)
	switch {
	case err == nil:
		s.value = value
	case key != s.key:
		// No earlier observation of this key to fall back on.
		s.value = ""
	}
	s.key, s.at, s.sampled = key, now(), true
	return s.value
}
