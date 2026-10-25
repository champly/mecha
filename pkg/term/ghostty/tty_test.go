package ghostty

import "testing"

func TestCanonicalTTY(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"spaces", "  \n", ""},
		{"question", "?", ""},
		{"double question", "??", ""},
		{"not a tty", "not a tty", ""},
		{"not a tty uppercase", "Not A TTY", ""},
		{"device name", "ttys001", "/dev/ttys001"},
		{"trimmed name", "  ttys001\n", "/dev/ttys001"},
		{"device path", "/dev/ttys001", "/dev/ttys001"},
		{"pts path", "pts/3", "/dev/pts/3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canonicalTTY(tt.in); got != tt.want {
				t.Errorf("canonicalTTY(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
