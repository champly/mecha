package shellquote

import (
	"os/exec"
	"testing"
)

func TestQuote(t *testing.T) {
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
		if got := Quote(c.in); got != c.want {
			t.Errorf("Quote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestQuoteRoundTrip feeds the quoted form to a real shell and checks the
// value survives unchanged — no expansion, no splitting, no substitution.
func TestQuoteRoundTrip(t *testing.T) {
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
		out, err := exec.Command("sh", "-c", "printf '%s' "+Quote(v)).Output()
		if err != nil {
			t.Fatalf("sh -c with %q: %v", v, err)
		}
		if string(out) != v {
			t.Errorf("round trip: got %q, want %q", out, v)
		}
	}
}
