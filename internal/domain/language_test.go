package domain

import "testing"

func TestLanguageCommands(t *testing.T) {
	for _, l := range []Language{LangGo, LangRust, LangJavaScript, LangTypeScript, LangPython} {
		cmds := LanguageCommands(l)
		if cmds["lint"] == "" || cmds["test"] == "" {
			t.Errorf("%s: expected non-empty lint/test, got %+v", l, cmds)
		}
	}
	if LanguageCommands(LangUnknown) != nil {
		t.Error("unknown language should return nil commands")
	}
	// A fresh map each call — mutating one must not affect the next.
	a := LanguageCommands(LangGo)
	a["lint"] = "mutated"
	if LanguageCommands(LangGo)["lint"] == "mutated" {
		t.Error("LanguageCommands must return a fresh map")
	}
}
