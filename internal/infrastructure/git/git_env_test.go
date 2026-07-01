package git

import (
	"slices"
	"testing"
)

func TestScrubHookEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"GIT_DIR=/repo/.git",
		"GIT_INDEX_FILE=/repo/.git/index",
		"GIT_WORK_TREE=/repo",
		"HOME=/home/x",
		"GIT_PREFIX=sub/",
		"GIT_AUTHOR_NAME=keep-me", // not a hook working-var; must survive
	}
	got := scrubHookEnv(in)
	for _, bad := range []string{"GIT_DIR=/repo/.git", "GIT_INDEX_FILE=/repo/.git/index", "GIT_WORK_TREE=/repo", "GIT_PREFIX=sub/"} {
		if slices.Contains(got, bad) {
			t.Errorf("scrubHookEnv left %q", bad)
		}
	}
	for _, keep := range []string{"PATH=/usr/bin", "HOME=/home/x", "GIT_AUTHOR_NAME=keep-me"} {
		if !slices.Contains(got, keep) {
			t.Errorf("scrubHookEnv dropped %q", keep)
		}
	}
}
