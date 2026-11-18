package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"
)

type Mode string

const (
	ModeReadOnly       Mode = "read-only"
	ModeWorkspaceWrite Mode = "workspace-write"
	ModeFullAccess     Mode = "full-access"
)

func parseMode(s string) (Mode, error) {
	switch m := Mode(strings.TrimSpace(s)); m {
	case ModeReadOnly, ModeWorkspaceWrite, ModeFullAccess:
		return m, nil
	default:
		return "", fmt.Errorf("sandbox: mode must be %q, %q, or %q", ModeReadOnly, ModeWorkspaceWrite, ModeFullAccess)
	}
}

type policy struct {
	Mode           Mode
	Network        bool
	WritableRoots  []string
	ExtraWritable  []string
	DenyRead       []string
	ReadOnly       []string
	ProtectedNames []string
	UnixSockets    []string
	ProxyPort      int
	LogTag         string
}

func validateRoot(root, home string) error {
	if root == "/" {
		return fmt.Errorf("sandbox: / cannot be a command directory")
	}
	if home == "" {
		return nil
	}
	for _, protected := range []string{home, filepath.Join(home, "Library")} {
		if within(root, protected) {
			return fmt.Errorf("sandbox: %q cannot be a command directory because it contains %s; choose a project folder inside it", root, protected)
		}
	}
	return nil
}

var defaultProtectedNames = []string{
	".vscode", ".idea", ".zed", ".fleet", ".devcontainer",

	".envrc", ".mise.toml", "mise.toml", ".mise",
	".bashrc", ".bash_profile", ".zshrc", ".zprofile", ".zshenv", ".profile",

	".husky", ".githooks", ".pre-commit-config.yaml", "lefthook.yml", ".lefthook.yml", "lefthook.yaml",
	".gitconfig", ".gitmodules",

	".ripgreprc",
}

func validProtectedName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("sandbox: invalid protected name %q", name)
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return fmt.Errorf("sandbox: protected name %q may hold only letters, digits, dots, dashes, and underscores", name)
		}
	}
	return nil
}

func defaultDenyRead(home string) []string {
	names := []string{
		".ssh", ".gnupg", ".aws", ".azure", ".kube", ".netrc", ".config/gcloud", ".docker/config.json",
		".git-credentials", ".cargo/credentials", ".cargo/credentials.toml", ".pypirc", ".gem/credentials",
		".vault-token", ".terraform.d/credentials.tfrc.json", ".password-store", ".config/op", ".codex/auth.json",
		".config/gh", ".config/hub", ".config/glab-cli", ".config/git/credentials", ".config/github-copilot",
		".npmrc", ".yarnrc", ".yarnrc.yml", ".config/configstore", ".composer/auth.json", ".config/composer/auth.json",
		".m2/settings.xml", ".m2/settings-security.xml", ".gradle/gradle.properties", ".nuget/NuGet/NuGet.Config",
		".pgpass", ".my.cnf", ".boto", ".s3cfg", ".databrickscfg", ".oci", ".config/doctl", ".config/hcloud",
		".fly", ".kaggle", ".huggingface/token", ".cache/huggingface/token", ".config/stripe", ".claude/.credentials.json",

		".zsh_history", ".bash_history", ".python_history", ".node_repl_history",

		"Library/Keychains", "Library/Cookies", "Library/Messages", "Library/Mail", "Library/Safari",
		"Library/Application Support/Google/Chrome", "Library/Application Support/Firefox",
		"Library/Application Support/BraveSoftware", "Library/Application Support/Microsoft Edge",
		"Library/Application Support/Arc",
	}
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = filepath.Join(home, name)
	}
	return out
}

func defaultWritableCaches(home string) []string {
	names := []string{".npm/_cacache", ".npm/_logs"}
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = filepath.Join(home, name)
	}
	return out
}
