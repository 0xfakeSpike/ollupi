package tools

type Prepared struct {
	Cwd             string            `json:"cwd"`
	SettingsDigest  string            `json:"settingsDigest"`
	ArgumentsDigest string            `json:"argumentsDigest"`
	Program         string            `json:"program,omitempty"`
	Args            []string          `json:"args,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	TimeoutSec      int               `json:"timeoutSec,omitempty"`
	Sandboxed       bool              `json:"sandboxed,omitempty"`
	Escalated       bool              `json:"escalated,omitempty"`
	Justification   string            `json:"justification,omitempty"`
	Files           []FileChange      `json:"files,omitempty"`
}

type FileChange struct {
	Path    string `json:"path"`
	Before  string `json:"before,omitempty"`
	After   string `json:"after"`
	Diff    string `json:"diff"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}
