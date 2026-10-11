package agentd

import "testing"

func TestNormalizeTask(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"single line untouched", "fix the bug", "fix the bug"},
		{"trailing newline (heredoc)", "fix the bug\n", "fix the bug"},
		{"leading whitespace", "  fix the bug", "fix the bug"},
		{"multi line joined", "line one\nline two", "line one line two"},
		{"crlf joined", "line one\r\nline two", "line one line two"},
		{"lone cr joined", "line one\rline two", "line one line two"},
		{"blank lines collapse to spaces", "a\n\nb", "a  b"},
		{"only whitespace", "  \n  ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeTask(c.in); got != c.want {
				t.Errorf("normalizeTask(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
