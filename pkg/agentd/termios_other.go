//go:build !darwin && !linux

package agentd

import (
	"errors"
	"os"
)

func ptyRaw(*os.File) (bool, error) {
	return false, errors.New("termios probe is unsupported on this platform")
}
