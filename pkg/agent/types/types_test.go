package types

import (
	"reflect"
	"testing"
)

func TestBuildArgsBoolTrue(t *testing.T) {
	args := BuildArgs(nil, map[string]any{"yolo": true})
	want := []string{"--yolo"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("BuildArgs = %v, want %v", args, want)
	}
}

// A default true must be overridable with false: the flag disappears instead
// of producing a "--key false" pair (which pflag-style CLIs read as "flag set
// plus a stray positional argument").
func TestBuildArgsBoolFalseOmitsFlag(t *testing.T) {
	defaults := map[string]any{"skip": true, "model": "x"}
	args := BuildArgs(map[string]any{"skip": false}, defaults)
	want := []string{"--model", "x"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("BuildArgs = %v, want %v", args, want)
	}
}

func TestBuildArgsSortedAndUserWins(t *testing.T) {
	args := BuildArgs(
		map[string]any{"b": "user"},
		map[string]any{"a": 1, "b": "default"},
	)
	want := []string{"--a", "1", "--b", "user"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("BuildArgs = %v, want %v", args, want)
	}
}
