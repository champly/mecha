// Package term provides terminal pane management across different backends.
//
// Usage:
//
//	backend, err := term.New()
package term

import (
	"errors"

	"github.com/champly/mecha/pkg/term/driver"
	"github.com/champly/mecha/pkg/term/ghostty"
	"github.com/champly/mecha/pkg/term/iterm2"
	"github.com/champly/mecha/pkg/term/tmux"
)

// Re-exported types for convenience.
type (
	Backend = driver.Backend
	Spec    = driver.Spec
	Handle  = driver.Handle
)

// ErrUnsupported is returned when no provider matches the current environment.
var ErrUnsupported = errors.New("term: unsupported terminal environment")

var factories = []struct {
	match func() bool
	ctor  func() (Backend, error)
}{
	{tmux.Match, tmux.New},
	{iterm2.Match, iterm2.New},
	{ghostty.Match, ghostty.New},
}

// New selects the first provider that matches the current environment.
func New() (Backend, error) {
	for _, f := range factories {
		if f.match() {
			return f.ctor()
		}
	}
	return nil, ErrUnsupported
}
