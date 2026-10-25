package ghostty

import (
	"strings"
	"testing"
)

func TestQuoteAppleScript(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", `""`},
		{"plain", `"plain"`},
		{`a\nb`, `"a\\nb"`},
		{`a\b`, `"a\\b"`},
		{`"quoted"`, `"\"quoted\""`},
		{`a\"b`, `"a\\\"b"`},
		{`a\\b`, `"a\\\\b"`},
	}
	for _, tt := range tests {
		if got := quoteAppleScript(tt.in); got != tt.want {
			t.Errorf("quoteAppleScript(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestInitialInputEscapesCommand(t *testing.T) {
	script := initialInput(`printf 'a\nb'`, "newTerminal")
	if !strings.Contains(script, `"printf 'a\\nb'"`) {
		t.Errorf("initialInput did not escape backslash: %q", script)
	}
	if strings.Contains(script, `printf 'a\nb'`) {
		t.Errorf("initialInput leaked an unescaped backslash-newline: %q", script)
	}
}

func TestGeneratedScriptsHaveNoFocusKeywords(t *testing.T) {
	scripts := []string{
		listTerminalsScript(),
		anchorSpawnScript("ID-ANCHOR", "mecha agentd"),
		splitSpawnScript("ID-CHILD", "mecha agentd"),
	}
	for _, script := range scripts {
		lower := strings.ToLower(script)
		if strings.Contains(lower, "front window") {
			t.Errorf("generated script references front window: %q", script)
		}
		if strings.Contains(lower, "focused terminal") {
			t.Errorf("generated script references focused terminal: %q", script)
		}
	}
}
