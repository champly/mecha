//go:build darwin

package agentd

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func ptyRaw(ptmx *os.File) (bool, error) {
	t, err := unix.IoctlGetTermios(int(ptmx.Fd()), unix.TIOCGETA)
	if err != nil {
		return false, fmt.Errorf("read pty termios: %w", err)
	}
	return t.Lflag&unix.ICANON == 0, nil
}
