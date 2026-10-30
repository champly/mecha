package driver

import (
	"testing"
)

func TestBuildCommand(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		want string
	}{
		{"empty command", Spec{WorkDir: "/tmp"}, ""},
		{"command only", Spec{Command: []string{"mecha", "agentd"}}, "mecha agentd"},
		{
			"workdir does not add a shell prefix",
			Spec{WorkDir: "/tmp/my proj", Command: []string{"mecha", "agentd", "--dir", "/tmp/my proj"}},
			"mecha agentd --dir '/tmp/my proj'",
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
