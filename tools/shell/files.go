package shell

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

const (
	ToolEditFile  = "edit_file"
	ToolWriteFile = "write_file"

	maxFileBytes = 2 << 20
)

type Edit struct {
	OldText    string `json:"old_text" jsonschema:"Exact text to replace, including whitespace; it must appear exactly once unless replace_all is set."`
	NewText    string `json:"new_text" jsonschema:"Replacement text."`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"Replace every occurrence of old_text."`
}

type EditFile struct {
	Path  string `json:"path" jsonschema:"File to edit, relative to the project root or absolute inside it."`
	Edits []Edit `json:"edits" jsonschema:"Replacements applied in order, each to the result of the previous one."`
}

func (Edit) Constrain(s *jsonschema.Schema) {
	s.Properties["old_text"].MinLength = jsonschema.Ptr(1)
}

func (EditFile) Constrain(s *jsonschema.Schema) {
	s.Properties["path"].MinLength = jsonschema.Ptr(1)
	s.Properties["edits"].MinItems = jsonschema.Ptr(1)
}

type WriteFile struct {
	Path    string `json:"path" jsonschema:"File to create or overwrite, relative to the project root or absolute inside it; missing folders are created."`
	Content string `json:"content" jsonschema:"The whole new content of the file."`
}

func (WriteFile) Constrain(s *jsonschema.Schema) {
	s.Properties["path"].MinLength = jsonschema.Ptr(1)
}

type FileOutput struct {
	Path    string `json:"path"`
	Created bool   `json:"created,omitempty"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

func (r *Runner) fileTools() []*tools.Spec {
	annotations := func(title string) *tools.Annotations {
		return &tools.Annotations{Title: title, DestructiveHint: tools.Hint(true), OpenWorldHint: tools.Hint(false)}
	}
	where := "Writes only inside the project (outside a project: inside " + strings.Join(r.box.Roots(), ", ") + "), never to .git or protected settings files. "
	return []*tools.Spec{{
		Name: ToolEditFile,
		Description: "Edit a text file by exact replacements. Read the file first so old_text matches it exactly. " + where +
			"Prefer this over shell commands for changing files: the user sees the diff.",
		InputSchema:       tools.InputSchema[EditFile](),
		Annotations:       annotations("Edit file"),
		PermissionContent: r.fileContent,
		Local:             &tools.Local{Prepare: r.prepareFile, Run: r.runFile},
	}, {
		Name:              ToolWriteFile,
		Description:       "Create a text file or replace all of its content. " + where + "To change part of an existing file use " + ToolEditFile + ".",
		InputSchema:       tools.InputSchema[WriteFile](),
		Annotations:       annotations("Write file"),
		PermissionContent: r.fileContent,
		Local:             &tools.Local{Prepare: r.prepareFile, Run: r.runFile},
	}}
}

type fileChange struct {
	abs, rel string
	exists   bool
	mode     fs.FileMode
	before   string
	after    string
}

func (r *Runner) change(ctx context.Context, args map[string]any) (dirs, fileChange, error) {
	d, err := r.dirs(tools.CallerOf(ctx))
	if err != nil {
		return dirs{}, fileChange{}, err
	}
	raw, _ := args["path"].(string)
	if strings.TrimSpace(raw) == "" {
		return dirs{}, fileChange{}, fmt.Errorf("shell: argument path must be a non-empty string")
	}
	path := raw
	if !filepath.IsAbs(path) {
		path = filepath.Join(d.cwd, path)
	}
	abs, err := sandbox.CanonicalPath(path, "")
	if err != nil {
		return dirs{}, fileChange{}, err
	}
	if err := r.box.CheckWrite(d.roots, abs); err != nil {
		return dirs{}, fileChange{}, err
	}
	c := fileChange{abs: abs, mode: 0o644}
	for _, root := range d.roots {
		if sandbox.Within(root, abs) {
			c.rel, _ = filepath.Rel(root, abs)
			break
		}
	}
	c.rel = filepath.ToSlash(c.rel)
	switch st, err := os.Stat(abs); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return dirs{}, fileChange{}, err
	case !st.Mode().IsRegular():
		return dirs{}, fileChange{}, fmt.Errorf("shell: %s is not a regular file", c.rel)
	case st.Size() > maxFileBytes:
		return dirs{}, fileChange{}, fmt.Errorf("shell: %s has %d bytes; files over %d bytes are not edited here", c.rel, st.Size(), maxFileBytes)
	default:
		body, err := os.ReadFile(abs)
		if err != nil {
			return dirs{}, fileChange{}, err
		}
		if !utf8.Valid(body) {
			return dirs{}, fileChange{}, fmt.Errorf("shell: %s is not UTF-8 text", c.rel)
		}
		c.exists, c.mode, c.before = true, st.Mode().Perm(), string(body)
	}
	if c.after, err = newContent(c, args); err != nil {
		return dirs{}, fileChange{}, err
	}
	if len(c.after) > maxFileBytes {
		return dirs{}, fileChange{}, fmt.Errorf("shell: the new %s would have %d bytes; at most %d are written", c.rel, len(c.after), maxFileBytes)
	}
	return d, c, nil
}

func newContent(c fileChange, args map[string]any) (string, error) {
	if _, ok := args["content"]; ok {
		w, err := tools.DecodeArguments[WriteFile](args)
		return w.Content, err
	}
	e, err := tools.DecodeArguments[EditFile](args)
	if err != nil {
		return "", err
	}
	if !c.exists {
		return "", fmt.Errorf("shell: %s does not exist; create it with %s", c.rel, ToolWriteFile)
	}
	text := c.before
	for i, edit := range e.Edits {
		n := strings.Count(text, edit.OldText)
		switch {
		case n == 0:
			return "", fmt.Errorf("shell: edit %d: old_text was not found in %s; read the file again and copy the text exactly", i+1, c.rel)
		case n > 1 && !edit.ReplaceAll:
			return "", fmt.Errorf("shell: edit %d: old_text appears %d times in %s; add surrounding lines to make it unique, or set replace_all", i+1, n, c.rel)
		}
		text = strings.ReplaceAll(text, edit.OldText, edit.NewText)
	}
	if text == c.before {
		return "", fmt.Errorf("shell: the edits leave %s unchanged", c.rel)
	}
	return text, nil
}

func (c fileChange) frozen() tools.FileChange {
	diff, added, removed := unifiedDiff(c.rel, c.before, c.after, !c.exists)
	f := tools.FileChange{Path: c.rel, After: sha(c.after), Diff: diff, Added: added, Removed: removed}
	if c.exists {
		f.Before = sha(c.before)
	}
	return f
}

func (r *Runner) fileContent(ctx context.Context, args map[string]any) (tools.Content, error) {
	_, c, err := r.change(ctx, args)
	if err != nil {
		return tools.Content{}, err
	}
	return tools.Content{Text: c.frozen().Diff, Match: strings.ToLower(c.rel)}, nil
}

func (r *Runner) prepareFile(ctx context.Context, args map[string]any) (*tools.Prepared, error) {
	d, c, err := r.change(ctx, args)
	if err != nil {
		return nil, err
	}
	return &tools.Prepared{
		Cwd: d.cwd, SettingsDigest: r.settingsDigest(d), ArgumentsDigest: digest(args),
		Sandboxed: r.box.Enforced(), Files: []tools.FileChange{c.frozen()},
	}, nil
}

func (r *Runner) runFile(ctx context.Context, args map[string]any, p *tools.Prepared) (tools.Result, error) {
	fail := func(err error) (tools.Result, error) { return tools.Result{Err: err}, err }
	d, err := r.dirs(tools.CallerOf(ctx))
	if err != nil {
		return fail(err)
	}
	if err := r.validatePrepared(d, p, args); err != nil {
		return fail(err)
	}
	if len(p.Files) != 1 {
		return fail(fmt.Errorf("shell: incomplete prepared file change"))
	}
	_, c, err := r.change(ctx, args)
	if err != nil {
		return fail(fmt.Errorf("%w; the file changed after this edit was prepared, so nothing was written", err))
	}
	want := p.Files[0]
	if now := c.frozen(); now.Path != want.Path || now.Before != want.Before || now.After != want.After {
		return fail(fmt.Errorf("shell: %s changed after this edit was prepared, so nothing was written; read it again and redo the edit", want.Path))
	}
	if err := os.MkdirAll(filepath.Dir(c.abs), 0o755); err != nil {
		return fail(err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.abs), "."+filepath.Base(c.abs)+".*")
	if err != nil {
		return fail(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(c.after); err != nil {
		tmp.Close()
		return fail(err)
	}
	if err := errors.Join(tmp.Chmod(c.mode), tmp.Close()); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp.Name(), c.abs); err != nil {
		return fail(err)
	}
	return tools.Result{Output: FileOutput{Path: want.Path, Created: !c.exists, Added: want.Added, Removed: want.Removed}}, nil
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}
