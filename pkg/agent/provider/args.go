package provider

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
)

// MergeMap returns a new map with defaults overridden by user values.
func MergeMap[M ~map[K]V, K comparable, V any](user, defaults M) M {
	if len(defaults) == 0 {
		return maps.Clone(user)
	}
	r := maps.Clone(defaults)
	maps.Copy(r, user)
	return r
}

// BuildArgs merges user params over defaults and returns sorted --key value
// arguments. A true bool becomes a bare --key flag and a false bool is omitted,
// since "--key false" would leak "false" as a positional argument.
func BuildArgs(user, defaults map[string]any) []string {
	params := MergeMap(user, defaults)

	keys := slices.Sorted(maps.Keys(params))

	args := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		v := params[k]
		if b, ok := v.(bool); ok {
			if b {
				args = append(args, "--"+k)
			}
			continue
		}
		args = append(args, "--"+k, fmt.Sprint(v))
	}
	return args
}

// BuildEnv returns the process environment overlaid with defaults and user
// values (later layers win), sorted for deterministic output.
func BuildEnv(user, defaults map[string]string) []string {
	merged := make(map[string]string, len(defaults)+len(user))
	for _, e := range os.Environ() {
		if k, v, ok := strings.Cut(e, "="); ok {
			merged[k] = v
		}
	}
	maps.Copy(merged, MergeMap(user, defaults))

	keys := slices.Sorted(maps.Keys(merged))

	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+merged[k])
	}
	return env
}
