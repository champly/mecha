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
