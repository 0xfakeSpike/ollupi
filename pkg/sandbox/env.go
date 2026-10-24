package sandbox

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var baseEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TERM", "LANG", "TZ"}

func Environ(host []string, passthrough []string, extra map[string]string) []string {
	allowed := map[string]bool{}
	for _, k := range append(append([]string(nil), baseEnv...), passthrough...) {
		allowed[k] = true
	}
	values := map[string]string{}
	for _, pair := range host {
		k, v, ok := strings.Cut(pair, "=")
		if ok && (allowed[k] || strings.HasPrefix(k, "LC_")) {
			values[k] = v
		}
	}
	for k, v := range extra {
		values[k] = v
	}
	out := make([]string, 0, len(values))
	for k, v := range values {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func ValidateEnvNames(names []string) error {
	for _, k := range names {
		if !envName.MatchString(k) {
			return fmt.Errorf("sandbox: invalid environment variable name %q", k)
		}
	}
	return nil
}
