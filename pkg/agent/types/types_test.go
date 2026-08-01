package types

import (
	"reflect"
	"strings"
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

func TestParseHook(t *testing.T) {
	eventMap := map[string]string{"Stop": EventStop}

	t.Run("resolves event and session id", func(t *testing.T) {
		e, err := ParseHook("claude", []byte(`{"hook_event_name":"Stop","session_id":"s1"}`), eventMap, nil)
		if err != nil {
			t.Fatalf("ParseHook: %v", err)
		}
		if e.Event != EventStop || e.SessionID != "s1" {
			t.Errorf("event = %+v, want Stop with session s1", e)
		}
	})

	t.Run("extract fills event-specific fields", func(t *testing.T) {
		e, err := ParseHook("claude", []byte(`{"hook_event_name":"Stop","last_assistant_message":"hi"}`), eventMap,
			func(m map[string]any, e *HookEvent) {
				if msg, ok := m["last_assistant_message"].(string); ok {
					e.Output = msg
				}
			})
		if err != nil {
			t.Fatalf("ParseHook: %v", err)
		}
		if e.Output != "hi" {
			t.Errorf("Output = %q, want %q", e.Output, "hi")
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		_, err := ParseHook("claude", []byte(`{not json`), eventMap, nil)
		if err == nil || !strings.Contains(err.Error(), "claude: parse hook event") {
			t.Errorf("expected prefixed parse error, got %v", err)
		}
	})

	t.Run("missing hook_event_name", func(t *testing.T) {
		_, err := ParseHook("codex", []byte(`{"session_id":"x"}`), eventMap, nil)
		if err == nil || !strings.Contains(err.Error(), "codex: hook_event_name missing") {
			t.Errorf("expected prefixed missing-field error, got %v", err)
		}
	})

	t.Run("unknown event", func(t *testing.T) {
		_, err := ParseHook("pi", []byte(`{"hook_event_name":"Nope"}`), eventMap, nil)
		if err == nil || !strings.Contains(err.Error(), `pi: unknown hook event "Nope"`) {
			t.Errorf("expected prefixed unknown-event error, got %v", err)
		}
	})
}

func TestBuildEnv(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("SHARED", "base")

	env := BuildEnv(
		map[string]string{"SHARED": "user", "USER_ONLY": "u"},
		map[string]string{"SHARED": "default", "DEF_ONLY": "d"},
	)

	lookup := map[string]string{}
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			lookup[k] = v
		}
	}

	if lookup["SHARED"] != "user" {
		t.Errorf("SHARED = %q, want user to override default", lookup["SHARED"])
	}
	if lookup["DEF_ONLY"] != "d" || lookup["USER_ONLY"] != "u" {
		t.Errorf("missing merged entries: %v", lookup)
	}
	if lookup["PATH"] != "/usr/bin" {
		t.Errorf("PATH = %q, want inherited os.Environ value", lookup["PATH"])
	}
	for i := 1; i < len(env); i++ {
		if env[i-1] > env[i] {
			t.Errorf("env not sorted: %q before %q", env[i-1], env[i])
		}
	}
	if v, ok := lookup["NOPE"]; ok {
		t.Errorf("unexpected env entry NOPE=%q", v)
	}
}
