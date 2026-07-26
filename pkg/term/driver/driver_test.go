package driver

import (
	"os/exec"
	"testing"
)

func TestQuoteShell(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", `''`},
		{"simple", "simple"},
		{"a/b_c:d", "a/b_c:d"},
		{"$HOME/x", `'$HOME/x'`},
		{"`whoami`", "'`whoami`'"},
		{"it's", `'it'\''s'`},
		{"a b", `'a b'`},
		{"a\nb", "'a\nb'"},
	}
	for _, c := range cases {
		if got := QuoteShell(c.in); got != c.want {
			t.Errorf("QuoteShell(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestQuoteShellRoundTrip feeds the quoted form to a real shell and checks
// the value survives unchanged — no expansion, no splitting, no substitution.
func TestQuoteShellRoundTrip(t *testing.T) {
	values := []string{
		"$HOME/.mecha/config yaml",
		"`touch /tmp/mecha-pwned`",
		"$(touch /tmp/mecha-pwned)",
		"it's a path",
		"a;b|&c>d",
		"line1\nline2",
		`back\slash`,
	}
	for _, v := range values {
		out, err := exec.Command("sh", "-c", "printf '%s' "+QuoteShell(v)).Output()
		if err != nil {
			t.Fatalf("sh -c with %q: %v", v, err)
		}
		if string(out) != v {
			t.Errorf("round trip: got %q, want %q", out, v)
		}
	}
}
