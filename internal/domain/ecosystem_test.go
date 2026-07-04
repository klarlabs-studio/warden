package domain

import "testing"

func TestComposeConfig_MonorepoPathScopedAndPrefixed(t *testing.T) {
	cmds, steps := ComposeConfig([]Ecosystem{
		{Lang: LangGo, Path: "apps/api"},
		{Lang: LangTypeScript, Path: "web"},
	}, true)

	if cmds["apps-api-test"] != "cd apps/api && go test ./..." {
		t.Errorf("go test not path-scoped: %q", cmds["apps-api-test"])
	}
	if got := cmds["web-lint"]; got == "" || got[:3] != "cd " {
		t.Errorf("web lint not path-scoped: %q", got)
	}
	if cmds["security-scan"] == "" {
		t.Error("nox security-scan should be added when hasNox")
	}
	// pre_push runs tests + lints + security; pre_commit is lints only.
	if len(steps["pre_commit"]) != 2 {
		t.Errorf("pre_commit = %v, want 2 lints", steps["pre_commit"])
	}
	if len(steps["pre_push"]) != 5 { // 2 test + 2 lint + security
		t.Errorf("pre_push = %v, want 5", steps["pre_push"])
	}
}

func TestComposeConfig_SingleRootNoPrefixNoNox(t *testing.T) {
	cmds, _ := ComposeConfig([]Ecosystem{{Lang: LangGo, Path: "."}}, false)
	if _, ok := cmds["lint"]; !ok {
		t.Error("single root ecosystem should use unprefixed 'lint'")
	}
	if cmds["test"] != "go test ./..." {
		t.Errorf("root test should not be path-scoped: %q", cmds["test"])
	}
	if _, ok := cmds["security-scan"]; ok {
		t.Error("no security-scan without nox")
	}
}
