package sandbox

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"
)

type Config struct {
	Mode           string
	Network        bool
	Domains        DomainRules
	Roots          []string
	DenyRead       []string
	ReadOnly       []string
	ProtectedNames []string
	EnvMarker      string
	ExtraWritable  []string
	UnixSockets    []string
	EnvPassthrough []string
	MaxOutputBytes int
}

type Command struct {
	Program     string
	Args        []string
	Cwd         string
	Env         map[string]string
	Timeout     time.Duration
	Roots       []string
	Unsandboxed bool
}

type Result struct {
	Stdout, Stderr string
	ExitCode       int
	Signal         string
	Truncated      bool
	Denials        []string
}

var seatbeltSupported = defaultSeatbeltSupported

func defaultSeatbeltSupported() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("the sandbox needs macOS (this host is %s)", runtime.GOOS)
	}
	return nil
}

func resolveMode(mode string) (Mode, error) {
	m := ModeWorkspaceWrite
	if mode != "" {
		var err error
		if m, err = parseMode(mode); err != nil {
			return "", err
		}
	}
	if m != ModeFullAccess {
		if err := seatbeltSupported(); err != nil {
			return "", fmt.Errorf("sandbox: mode %q: %w; set the mode to %q to run commands without a sandbox", m, err, ModeFullAccess)
		}
	}
	return m, nil
}

type Sandbox struct {
	home           string
	policy         policy
	rules          compiledRules
	envPassthrough []string
	maxOutputBytes int
	denials        *denialLog
	envMarker      string
}

func New(c Config) (*Sandbox, error) {
	if len(c.Roots) == 0 {
		return nil, fmt.Errorf("sandbox: at least one command directory is required")
	}
	mode, err := resolveMode(c.Mode)
	if err != nil {
		return nil, err
	}
	if mode != ModeFullAccess && runtime.GOOS == "darwin" {
		if _, err := os.Stat(seatbeltExecutable); err != nil {
			return nil, fmt.Errorf("sandbox: %w", err)
		}
	}
	if c.MaxOutputBytes < 0 {
		return nil, fmt.Errorf("sandbox: max output bytes must be >= 0")
	}
	if err := ValidateEnvNames(c.EnvPassthrough); err != nil {
		return nil, err
	}
	if c.EnvMarker != "" {
		if err := ValidateEnvNames([]string{c.EnvMarker}); err != nil {
			return nil, err
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("sandbox: the rules name folders in the home directory: %w", err)
	}
	if home, err = canonicalPath(home, ""); err != nil {
		return nil, err
	}
	for _, name := range c.ProtectedNames {
		if err := validProtectedName(name); err != nil {
			return nil, err
		}
	}
	p, err := pathsOf(c, mode, home)
	if err != nil {
		return nil, err
	}
	rules, err := compileRules(c.Domains)
	if err != nil {
		return nil, err
	}
	if len(rules.allow) == 0 && len(rules.deny) > 0 {
		return nil, fmt.Errorf("sandbox: denied_domains needs allowed_domains; without it the network is either off or open")
	}
	s := &Sandbox{
		home:           home,
		policy:         p,
		rules:          rules,
		envPassthrough: append([]string(nil), c.EnvPassthrough...),
		maxOutputBytes: c.MaxOutputBytes,
		envMarker:      c.EnvMarker,
	}
	if s.maxOutputBytes == 0 {
		s.maxOutputBytes = defaultMaxOutputBytes
	}
	if mode != ModeFullAccess && runtime.GOOS == "darwin" {
		l := newDenialLog()
		if err := l.start(); err == nil {
			s.denials = l
		}
	}
	return s, nil
}

func pathsOf(c Config, mode Mode, home string) (policy, error) {
	canonical := func(what string, paths []string) ([]string, error) {
		out := make([]string, 0, len(paths))
		for _, path := range paths {
			p, err := canonicalPath(path, home)
			if err != nil {
				return nil, fmt.Errorf("sandbox: %s: %w", what, err)
			}
			out = append(out, p)
		}
		return out, nil
	}
	p := policy{Mode: mode, Network: c.Network, ProtectedNames: append(slices.Clone(defaultProtectedNames), c.ProtectedNames...)}
	var err error
	if p.WritableRoots, err = canonical("command directory", c.Roots); err != nil {
		return policy{}, err
	}
	if mode != ModeFullAccess {
		for _, root := range p.WritableRoots {
			if err := validateRoot(root, home); err != nil {
				return policy{}, err
			}
		}
	}
	if p.DenyRead, err = canonical("deny_read", append(defaultDenyRead(home), c.DenyRead...)); err != nil {
		return policy{}, err
	}
	if p.ReadOnly, err = canonical("read_only", c.ReadOnly); err != nil {
		return policy{}, err
	}
	if p.ExtraWritable, err = canonical("extra_writable", append(defaultWritableCaches(home), c.ExtraWritable...)); err != nil {
		return policy{}, err
	}
	if p.UnixSockets, err = canonical("unix_sockets", c.UnixSockets); err != nil {
		return policy{}, err
	}
	for _, path := range p.UnixSockets {
		if _, err := seatbeltPathRegex(path); err != nil {
			return policy{}, err
		}
	}
	return p, nil
}

func (s *Sandbox) Close() error {
	if s.denials != nil {
		s.denials.close()
	}
	return nil
}

func (s *Sandbox) runMarker() string {
	if s.envMarker == "" {
		return "SANDBOX_RUN_ID"
	}
	return s.envMarker + "_RUN_ID"
}

func (s *Sandbox) proxied() bool { return s.policy.Network && len(s.rules.allow) > 0 }

func (s *Sandbox) Enforced() bool { return s.policy.Mode != ModeFullAccess }

func (s *Sandbox) Roots() []string { return append([]string(nil), s.policy.WritableRoots...) }

func (s *Sandbox) Fingerprint() any {
	return struct {
		Policy         policy
		Domains        DomainRules
		EnvPassthrough []string
		MaxOutputBytes int
	}{s.policy, s.rules.named(), s.envPassthrough, s.maxOutputBytes}
}

func (s *Sandbox) Summary() string {
	if !s.Enforced() {
		return "No sandbox: commands run with the user's full access to files and network"
	}
	var fs string
	switch s.policy.Mode {
	case ModeReadOnly:
		fs = "Sandbox: files are read-only"
	default:
		fs = "Sandbox: writes are allowed only inside the command directories, temp dirs, and npm's download cache; " +
			"Go, Cargo, pip, Yarn, pnpm and other module/build caches are read-only, so fetching dependencies that are not cached yet fails; " +
			"the settings files that other programs later run (" + protectedSummary(s.policy.ProtectedNames) + ") cannot be created anywhere, temp dirs included; " +
			"credential and token files (~/.ssh, ~/.aws and other cloud CLIs, ~/.netrc, ~/.npmrc, gh, git, Docker, Maven, Gradle, PostgreSQL credentials, ...) cannot be read"
	}
	switch {
	case s.proxied():
		allowed := strings.Join(s.rules.named().Allowed, ", ")
		if len(allowed) > 300 {
			allowed = allowed[:300] + ", ..."
		}
		return fs + "; network reaches only these hosts, through a proxy set in HTTP_PROXY, HTTPS_PROXY and ALL_PROXY: " + allowed +
			"; local Unix sockets (Docker, ssh-agent) are closed"
	case s.policy.Network:
		return fs + "; network is on, local Unix sockets (Docker, ssh-agent) are closed"
	}
	return fs + "; network is off"
}

func protectedSummary(names []string) string {
	shown := append([]string{".vscode", ".envrc", ".husky"}, names[len(defaultProtectedNames):]...)
	return strings.Join(shown, ", ") + ", ..."
}

func (s *Sandbox) Check(dir string) error {
	if !s.Enforced() {
		return nil
	}
	return validateRoot(dir, s.home)
}

func (s *Sandbox) commandRoots(c Command) ([]string, error) {
	if len(c.Roots) == 0 {
		return s.policy.WritableRoots, nil
	}
	for _, root := range c.Roots {
		canonical, err := canonicalPath(root, s.home)
		if err != nil {
			return nil, err
		}
		if canonical != root {
			return nil, fmt.Errorf("sandbox: command directory %q is not canonical (%q)", root, canonical)
		}
		if err := validateRoot(root, s.home); err != nil {
			return nil, err
		}
	}
	return c.Roots, nil
}

func (s *Sandbox) Run(ctx context.Context, c Command) (Result, error) {
	if c.Program == "" || c.Timeout <= 0 {
		return Result{}, fmt.Errorf("sandbox: incomplete command")
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	cmd, px, tagID, err := s.command(ctx, c)
	if err != nil {
		return Result{}, err
	}
	if px != nil {
		defer px.close()
	}
	res, err := runProcess(ctx, cmd, c.Program, c.Timeout, s.maxOutputBytes, s.runMarker())

	failed := err != nil || res.ExitCode != 0
	var denials []string
	if tagID != "" {
		denials = s.denials.collect(tagID, failed)
	}
	if px != nil {
		denials = append(denials, px.blockedHosts()...)
	}
	if !failed {
		denials = nil
	}
	if err != nil {
		if len(denials) > 0 {
			err = fmt.Errorf("%w; the sandbox denied: %s", err, strings.Join(denials, "; "))
		}
		return Result{}, err
	}
	res.Denials = denials
	return res, nil
}

func (s *Sandbox) command(ctx context.Context, c Command) (*exec.Cmd, *proxy, string, error) {
	if !s.Enforced() || c.Unsandboxed {
		cmd := exec.CommandContext(ctx, c.Program, c.Args...)
		cmd.Dir = c.Cwd
		cmd.Env = environ(os.Environ(), s.envPassthrough, c.Env)
		return cmd, nil, "", nil
	}
	roots, err := s.commandRoots(c)
	if err != nil {
		return nil, nil, "", err
	}
	policy := s.policy
	policy.WritableRoots = roots
	env := map[string]string{}
	maps.Copy(env, c.Env)
	if s.envMarker != "" {
		env[s.envMarker] = "seatbelt"
		if !policy.Network {
			env[s.envMarker+"_NETWORK_DISABLED"] = "1"
		}
	}
	var px *proxy
	if s.proxied() {
		if px, err = startProxy(s.rules); err != nil {
			return nil, nil, "", err
		}
		policy.ProxyPort = px.port()
		maps.Copy(env, px.env())
		if own := c.Env["JAVA_TOOL_OPTIONS"]; own != "" {
			env["JAVA_TOOL_OPTIONS"] = own + " " + env["JAVA_TOOL_OPTIONS"]
		}
	}
	var tagID string
	if s.denials != nil && s.denials.running() {
		tagID, policy.LogTag = s.denials.newTag()
	}
	args, err := seatbeltArgs(policy, seatbeltTempDirs(), append([]string{c.Program}, c.Args...))
	if err != nil {
		if tagID != "" {
			s.denials.collect(tagID, false)
		}
		if px != nil {
			px.close()
		}
		return nil, nil, "", err
	}

	cmd := exec.CommandContext(ctx, seatbeltExecutable, args...)
	cmd.Dir = c.Cwd
	cmd.Env = environ(os.Environ(), s.envPassthrough, env)
	return cmd, px, tagID, nil
}
