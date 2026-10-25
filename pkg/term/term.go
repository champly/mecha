// Package term provides terminal pane management across different backends.
package term

import (
	"errors"
	"fmt"
	"strings"

	"github.com/champly/mecha/pkg/term/driver"
	"github.com/champly/mecha/pkg/term/ghostty"
	"github.com/champly/mecha/pkg/term/headless"
	"github.com/champly/mecha/pkg/term/iterm2"
	"github.com/champly/mecha/pkg/term/tmux"
)

// Re-exported types for convenience.
type (
	Backend = driver.Backend
	Spec    = driver.Spec
	Handle  = driver.Handle
)

// Options selects a terminal backend.
//
// The zero value (or no Options at all) preserves the historical behaviour:
// auto-detect tmux, iTerm2, or Ghostty and fail with ErrUnsupported otherwise.
type Options struct {
	// Kind is "auto" (default), "headless", "tmux", "iterm2", or "ghostty".
	Kind string
	// AllowHeadless makes "auto" fall back to the headless backend when no
	// interactive terminal is detected. It has no effect when Kind is set
	// explicitly.
	AllowHeadless bool
}

// ErrUnsupported is returned when no provider matches the current environment.
var ErrUnsupported = errors.New("term: unsupported terminal environment")

var factories = []struct {
	kind  string
	match func() bool
	ctor  func() (Backend, error)
}{
	{"tmux", tmux.Match, tmux.New},
	{"iterm2", iterm2.Match, iterm2.New},
	{"ghostty", ghostty.Match, ghostty.New},
}

// New selects a terminal backend. With no options it keeps the original
// auto-detection behaviour; headless is only used when explicitly requested
// (Kind == "headless") or allowed as an auto fallback (AllowHeadless).
func New(opts ...Options) (Backend, error) {
	var o Options
	for _, opt := range opts {
		if opt.Kind != "" {
			o.Kind = opt.Kind
		}
		o.AllowHeadless = o.AllowHeadless || opt.AllowHeadless
	}

	switch kind := strings.ToLower(strings.TrimSpace(o.Kind)); kind {
	case "", "auto":
		for _, f := range factories {
			if f.match() {
				return f.ctor()
			}
		}
		if o.AllowHeadless {
			return headless.New(headless.Config{}), nil
		}
		return nil, ErrUnsupported
	case "headless":
		return headless.New(headless.Config{}), nil
	default:
		for _, f := range factories {
			if f.kind != kind {
				continue
			}
			if !f.match() {
				return nil, fmt.Errorf("%w: %s", ErrUnsupported, kind)
			}
			return f.ctor()
		}
		return nil, fmt.Errorf("%w: unknown backend %q", ErrUnsupported, kind)
	}
}
