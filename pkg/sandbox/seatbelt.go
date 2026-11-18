package sandbox

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const seatbeltExecutable = "/usr/bin/sandbox-exec"

var (
	//go:embed seatbelt_base.sbpl
	seatbeltBase string
	//go:embed seatbelt_network.sbpl
	seatbeltNetwork string
	//go:embed seatbelt_preferences.sbpl
	seatbeltPreferences string
)

const seatbeltTLSTrust = `(allow mach-lookup (global-name "com.apple.TrustEvaluationAgent"))`

const seatbeltMDNS = `(allow network-outbound (remote unix-socket (regex #"^/private/var/run/mDNSResponder$")))
`

func writeUnixSocketAllows(b *strings.Builder, sockets []string) error {
	for _, path := range sockets {
		re, err := seatbeltPathRegex(path)
		if err != nil {
			return err
		}
		b.WriteString(`(allow network-outbound (remote unix-socket (regex #"` + re + `")))` + "\n")
	}
	return nil
}

func rootRule(action, root, pattern, message string) string {
	prefix := "/"
	if quoted, err := seatbeltRegexQuote(root); err == nil {
		prefix = "^" + caseless(quoted) + "/(.*/)?"
	}
	return "(" + action + " file-write* (regex #\"" + prefix + caselessPattern(pattern) + "\")" + message + ")"
}

func letterClass(r rune) string {
	return "[" + strings.ToLower(string(r)) + strings.ToUpper(string(r)) + "]"
}

func caseless(quoted string) string {
	var b strings.Builder
	escaped := false
	for _, r := range quoted {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			b.WriteRune(r)
			escaped = true
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			b.WriteString(letterClass(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func caselessPattern(pattern string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case strings.HasPrefix(pattern[i:], ".+"):
			b.WriteString(".+")
			i++
		case strings.HasPrefix(pattern[i:], "[^/]+"):
			b.WriteString("[^/]+")
			i += 4
		case c == '.':
			b.WriteString("\\.")
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			b.WriteString(letterClass(rune(c)))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

var dependencyDirs = []string{"node_modules", "vendor", "Pods"}

func seatbeltArgs(p policy, tempDirs []string, argv []string) ([]string, error) {
	profile, params, err := seatbeltProfile(p, tempDirs)
	if err != nil {
		return nil, err
	}
	args := []string{"-p", profile}
	for _, kv := range params {
		args = append(args, "-D", kv[0]+"="+kv[1])
	}
	args = append(args, "--")
	return append(args, argv...), nil
}

func seatbeltProfile(p policy, tempDirs []string) (string, [][2]string, error) {
	var params [][2]string
	param := func(prefix string, i int, path string) string {
		key := fmt.Sprintf("%s_%d", prefix, i)
		params = append(params, [2]string{key, path})
		return fmt.Sprintf(`(param "%s")`, key)
	}

	var b strings.Builder
	b.WriteString("(version 1)\n")
	if p.LogTag != "" {
		b.WriteString(strings.Replace(seatbeltBase, "(deny default)", `(deny default (with message "`+p.LogTag+`"))`, 1))
	} else {
		b.WriteString(seatbeltBase)
	}
	b.WriteString("\n; reads\n(allow file-read*)\n")
	b.WriteString(seatbeltPreferences)

	b.WriteString("\n(allow mach-lookup (global-name \"com.apple.bsd.dirhelper\"))\n")

	var writable []string
	if p.Mode == ModeWorkspaceWrite {
		writable = append(append(append(writable, p.WritableRoots...), p.ExtraWritable...), tempDirs...)
		b.WriteString("\n; writes\n(allow file-write*")
		for i, root := range writable {
			b.WriteString("\n  (subpath " + param("WRITABLE_ROOT", i, root) + ")")
		}
		b.WriteString(")\n")
	}

	msg := ""
	if p.LogTag != "" {
		msg = ` (with message "` + p.LogTag + `")`
	}

	b.WriteString("\n(allow system-socket (socket-domain AF_UNIX))\n")
	if p.Network {
		if err := networkRules(&b, p, msg); err != nil {
			return "", nil, err
		}
	}

	b.WriteString("\n; denies (keep last)\n")
	b.WriteString("(deny mach-lookup (xpc-service-name-prefix \"\"))\n")
	if p.Mode == ModeWorkspaceWrite {
		for _, root := range writable {
			for _, name := range p.ProtectedNames {
				b.WriteString(rootRule("deny", root, name+"(/|$)", msg) + "\n")
			}
			for _, dir := range dependencyDirs {
				b.WriteString(rootRule("allow", root, dir+"/", "") + "\n")
			}
		}
		b.WriteString("(deny file-clone (vnode-type DIRECTORY)" + msg + ")\n")
	}

	for i, path := range p.DenyRead {
		b.WriteString("(deny file-read* file-write* (subpath " + param("DENY_READ", i, path) + ")" + msg + ")\n")
	}
	for i, path := range p.ReadOnly {
		b.WriteString("(deny file-write* (subpath " + param("READ_ONLY", i, path) + ")" + msg + ")\n")
	}

	protected := append(append([]string(nil), p.DenyRead...), p.ReadOnly...)
	for i, dir := range protectedAncestors(protected, writable) {
		b.WriteString("(deny file-write-unlink (require-all (vnode-type DIRECTORY) (literal " + param("PROTECTED_ANCESTOR", i, dir) + ")))\n")
	}

	b.WriteString("(deny system-fcntl (fcntl-command 80 110))\n")
	return b.String(), params, nil
}

func networkRules(b *strings.Builder, p policy, msg string) error {
	proxied := p.ProxyPort != 0
	if proxied {
		fmt.Fprintf(b, "\n; network: the sandbox's proxy only\n(allow network-outbound (remote ip \"localhost:%d\"))\n", p.ProxyPort)
	} else {
		b.WriteString("\n; network\n(allow network-outbound)\n(allow network-inbound)\n")
	}
	b.WriteString(seatbeltNetwork)
	b.WriteString("\n" + seatbeltTLSTrust + "\n")
	if !proxied {
		b.WriteString("\n; unix sockets (after the network allow)\n")
		b.WriteString("(deny network-outbound (remote unix-socket)" + msg + ")\n")
		b.WriteString(seatbeltMDNS)
	}
	return writeUnixSocketAllows(b, p.UnixSockets)
}

func seatbeltPathRegex(path string) (string, error) {
	quoted, err := seatbeltRegexQuote(path)
	if err != nil {
		return "", err
	}
	return "^" + quoted + "(/|$)", nil
}

func seatbeltRegexQuote(path string) (string, error) {
	for _, r := range path {
		if r == '"' || r == '\\' || r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("sandbox: path %q contains a character the profile cannot quote", path)
		}
	}
	var b strings.Builder
	for _, r := range path {
		if strings.ContainsRune(`.[]()*+?{}|^$`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String(), nil
}

func protectedAncestors(denied, writable []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, path := range denied {
		for _, root := range writable {
			if !Within(root, path) || path == root {
				continue
			}
			for dir := filepath.Dir(path); Within(root, dir); dir = filepath.Dir(dir) {
				if !seen[dir] {
					seen[dir] = true
					out = append(out, dir)
				}
				if dir == root {
					break
				}
			}
		}
	}
	return out
}

func seatbeltTempDirs() []string {
	dirs := []string{"/private/tmp"}
	if tmp, err := canonicalPath(os.TempDir(), ""); err == nil && tmp != "/private/tmp" {
		dirs = append(dirs, tmp)
	}
	return dirs
}
