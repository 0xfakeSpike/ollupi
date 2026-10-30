package sandbox

import "testing"

func TestCheckWrite(t *testing.T) {
	s := &Sandbox{policy: policy{Mode: ModeWorkspaceWrite, WritableRoots: []string{"/p"}, ReadOnly: []string{"/p/skills"},
		ProtectedNames: append([]string{"AGENTS.md"}, defaultProtectedNames...)}}
	for path, ok := range map[string]bool{
		"/p/src/a.go": true, "/p/b.txt": true, "/q/a.go": false, "/p/.git/config": false, "/p/x/.git/HEAD": false,
		"/p/AGENTS.md": false, "/p/.vscode/settings.json": false, "/p/skills/x/SKILL.md": false,
	} {
		if err := s.CheckWrite([]string{"/p"}, path); (err == nil) != ok {
			t.Errorf("%s: %v", path, err)
		}
	}
	s.policy.Mode = ModeFullAccess
	if err := s.CheckWrite([]string{"/p"}, "/p/.git/config"); err != nil {
		t.Errorf("full access: %v", err)
	}
	s.policy.Mode = ModeReadOnly
	if err := s.CheckWrite([]string{"/p"}, "/p/a"); err == nil {
		t.Error("read-only mode allows a write")
	}
}

func TestCheckRead(t *testing.T) {
	s := &Sandbox{policy: policy{Mode: ModeFullAccess, DenyRead: []string{"/h/.ssh", "/a/data"}}}
	for path, ok := range map[string]bool{
		"/h/.ssh": false, "/h/.ssh/id_rsa": false, "/a/data/ollupi.sqlite": false, "/h/.sshx": true, "/a/config.go": true,
	} {
		if err := s.CheckRead(path); (err == nil) != ok {
			t.Errorf("%s: %v", path, err)
		}
	}
}
