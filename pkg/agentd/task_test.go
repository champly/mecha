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

func TestTaskInput(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		task      string
		want      string
	}{
		{
			name:      "codex uses bracketed paste",
			agentType: "codex",
			task:      "long task",
			want:      "\x1b[200~long task\x1b[201~",
		},
		{
			name:      "other agents unchanged",
			agentType: "claude",
			task:      "long task",
			want:      "long task",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := taskInput(tt.agentType, tt.task); got != tt.want {
				t.Errorf("taskInput() = %q, want %q", got, tt.want)
			}
		})
	}
}
