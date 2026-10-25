package ghostty

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ownTTY returns this process's controlling terminal as a canonical /dev/
// path. It asks ps first and falls back to tty on stdin.
func ownTTY() (string, error) {
	if out, err := exec.Command("ps", "-o", "tty=", "-p", strconv.Itoa(os.Getpid())).Output(); err == nil {
		if tty := canonicalTTY(string(out)); tty != "" {
			return tty, nil
		}
	}
	ttyCmd := exec.Command("tty")
	// tty must inspect this process's stdin, not os/exec's default /dev/null.
	ttyCmd.Stdin = os.Stdin
	if out, err := ttyCmd.Output(); err == nil {
		if tty := canonicalTTY(string(out)); tty != "" {
			return tty, nil
		}
	}
	return "", errors.New("term/ghostty: cannot determine controlling tty")
}

// canonicalTTY normalizes a tty name to a /dev/ path. It returns "" for
// output that does not name a terminal ("", "?", "??", "not a tty").
func canonicalTTY(raw string) string {
	s := strings.TrimSpace(raw)
	switch strings.ToLower(s) {
	case "", "?", "??", "not a tty":
		return ""
	}
	if strings.HasPrefix(s, "/dev/") {
		return s
	}
	return "/dev/" + s
}
