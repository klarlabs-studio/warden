package watch

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSamplerCachesForTTLAndKey(t *testing.T) {
	clock := time.Unix(0, 0)
	calls := 0
	answer, answerErr := "image-a", error(nil)
	s := &Sampler{
		TTL: 15 * time.Second,
		Sample: func(context.Context) (string, error) {
			calls++
			return answer, answerErr
		},
		now: func() time.Time { return clock },
	}
	ctx := context.Background()
	if got := s.Value(ctx, "cfg1"); got != "image-a" || calls != 1 {
		t.Fatalf("first sample: %q after %d calls", got, calls)
	}
	// A retag inside the TTL is not seen yet, and nothing is re-inspected.
	answer = "image-b"
	clock = clock.Add(14 * time.Second)
	if got := s.Value(ctx, "cfg1"); got != "image-a" || calls != 1 {
		t.Fatalf("within TTL: %q after %d calls", got, calls)
	}
	clock = clock.Add(time.Second)
	if got := s.Value(ctx, "cfg1"); got != "image-b" || calls != 2 {
		t.Fatalf("after TTL: %q after %d calls", got, calls)
	}
	// A different configuration is sampled at once, TTL or not.
	answer = "image-c"
	if got := s.Value(ctx, "cfg2"); got != "image-c" || calls != 3 {
		t.Fatalf("new key: %q after %d calls", got, calls)
	}
	// An inconclusive sample keeps the last observation of the same key...
	answer, answerErr = "", errors.New("timed out")
	clock = clock.Add(time.Minute)
	if got := s.Value(ctx, "cfg2"); got != "image-c" || calls != 4 {
		t.Fatalf("inconclusive: %q after %d calls", got, calls)
	}
	// ...and is not retried on every poll.
	if got := s.Value(ctx, "cfg2"); got != "image-c" || calls != 4 {
		t.Fatalf("inconclusive retried before TTL: %q after %d calls", got, calls)
	}
	// It never carries one configuration's environment over to another.
	if got := s.Value(ctx, "cfg3"); got != "" || calls != 5 {
		t.Fatalf("inconclusive new key: %q after %d calls", got, calls)
	}
}
