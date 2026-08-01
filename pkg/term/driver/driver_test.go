package driver

import (
	"os/exec"
	"strings"
	"testing"

	agenttypes "github.com/champly/mecha/pkg/agent/types"
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
		if got := agenttypes.QuoteShell(c.in); got != c.want {
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
		out, err := exec.Command("sh", "-c", "printf '%s' "+agenttypes.QuoteShell(v)).Output()
		if err != nil {
			t.Fatalf("sh -c with %q: %v", v, err)
		}
		if string(out) != v {
			t.Errorf("round trip: got %q, want %q", out, v)
		}
	}
}

func TestBuildCommand(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		want string
	}{
		{"empty command", Spec{WorkDir: "/tmp"}, ""},
		{"command only", Spec{Command: []string{"mecha", "agentd"}}, "mecha agentd"},
		{
			"workdir prefix",
			Spec{WorkDir: "/tmp/my proj", Command: []string{"mecha", "agentd"}},
			"cd '/tmp/my proj' && mecha agentd",
		},
		{
			"args needing quotes",
			Spec{Command: []string{"run", "a b"}},
			"run 'a b'",
		},
	}
	for _, c := range cases {
		if got := BuildCommand(c.spec); got != c.want {
			t.Errorf("%s: BuildCommand() = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestBuildCommandWorkDirRoundTrip runs the built command in a real shell and
// checks the cd prefix lands in the right directory even with spaces.
func TestBuildCommandWorkDirRoundTrip(t *testing.T) {
	dir := t.TempDir() + "/sub dir"
	if err := exec.Command("mkdir", "-p", dir).Run(); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-c", BuildCommand(Spec{
		WorkDir: dir,
		Command: []string{"pwd"},
	})).Output()
	if err != nil {
		t.Fatalf("sh -c: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != dir {
		t.Errorf("pwd = %q, want %q", got, dir)
	}
}
