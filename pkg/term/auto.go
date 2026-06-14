package term

import (
	"errors"
	"os"
	"os/exec"
	"strings"
)

var ErrUnsupportedEnvironment = errors.New("term: unsupported terminal environment")

type providerDetector struct {
	match   func() bool
	factory func() PaneBackend
}

// NewAutoProvider selects a terminal provider from current environment.
// Priority is tmux first, then iTerm2, then Ghostty.
func NewAutoProvider() (PaneBackend, error) {
	for _, detector := range providerDetectors() {
		if detector.match() {
			return detector.factory(), nil
		}
	}

	return nil, ErrUnsupportedEnvironment
}

func providerDetectors() []providerDetector {
	termProgram := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	return []providerDetector{
		{
			match: func() bool {
				return os.Getenv("TMUX") != "" && commandExists("tmux")
			},
			factory: func() PaneBackend {
				return NewTmuxProvider()
			},
		},
		{
			match: func() bool {
				return strings.Contains(termProgram, "iterm")
			},
			factory: func() PaneBackend {
				return NewITerm2Provider()
			},
		},
		{
			match: func() bool {
				return strings.Contains(termProgram, "ghostty")
			},
			factory: func() PaneBackend {
				return NewGhosttyProvider()
			},
		},
	}
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
