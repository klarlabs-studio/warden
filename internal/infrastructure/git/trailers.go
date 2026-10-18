package git

import (
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// Field separators for SpanTrailers' log format. git emits them between the
// parts it parsed, so trailer text is never split on spacing or colons warden
// would have to re-parse: git's own trailer parser (the one interpret-trailers
// uses) has already decided what is a key and what is a value.
const (
	trailerFieldSep = "\x1f" // between the SHA and the trailers, and key and value
	trailerSep      = "\x1e" // between trailers
)

// spanTrailersFormat prints each commit as SHA, US, then its trailer block with
// each trailer as key US value, separated by RS. unfold joins continuation
// lines; only drops the non-trailer lines of the block. Records are
// NUL-terminated by -z.
const spanTrailersFormat = "--format=%H" + "%x1f" +
	"%(trailers:only=true,unfold=true,separator=%x1e,key_value_separator=%x1f)"

// SpanTrailers returns the trailers of every commit in (base, head], including
// merge commits, newest first. An empty base reads head alone: a record that
// claims no span attests only its tip, so only the tip's declarations bear on it.
func (r *Repo) SpanTrailers(base, head string) ([]domain.CommitTrailers, error) {
	if head == "" {
		return nil, nil
	}
	args := []string{"log", "-z", "--no-show-signature", "--no-color", spanTrailersFormat}
	if base == "" {
		args = append(args, "--no-walk", head)
	} else {
		args = append(args, base+".."+head)
	}
	args = append(args, "--")
	out, err := runRawIn(r.Dir, args...)
	if err != nil {
		return nil, err
	}
	return parseSpanTrailers(out), nil
}

// parseSpanTrailers decodes SpanTrailers' log output. A trailer value cannot
// contain NUL, and a commit message that embeds the separator bytes can at
// most invent trailers on its own commit, which only adds declarations.
func parseSpanTrailers(out string) []domain.CommitTrailers {
	var commits []domain.CommitTrailers
	for rec := range strings.SplitSeq(out, "\x00") {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		sha, block, _ := strings.Cut(rec, trailerFieldSep)
		c := domain.CommitTrailers{Commit: strings.TrimSpace(sha)}
		for t := range strings.SplitSeq(strings.TrimRight(block, "\n"), trailerSep) {
			key, value, ok := strings.Cut(t, trailerFieldSep)
			key = strings.TrimSpace(key)
			if !ok || key == "" {
				continue
			}
			c.Trailers = append(c.Trailers, domain.Trailer{Key: key, Value: strings.TrimSpace(value)})
		}
		commits = append(commits, c)
	}
	return commits
}
