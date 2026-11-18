package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
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

func (r *Provider) fileTools() []kit.Spec {
	annotations := func(title string) tool.Annotations {
		return tool.Annotations{Title: title, DestructiveHint: kit.Hint(true), OpenWorldHint: kit.Hint(false)}
	}
	where := "Writes only inside the project (outside a project: inside " + strings.Join(r.box.Roots(), ", ") + "), never to protected settings files. "
	return []kit.Spec{{
		Name: ToolEditFile,
		Description: "Edit a text file by exact replacements. Read the file first so old_text matches it exactly. " + where +
			"Prefer this over shell commands for changing files: the user sees the diff.",
		InputSchema: kit.InputSchema[EditFile](),
		Annotations: annotations("Edit file"),
		Content:     r.fileContent,
		Prepare:     r.locals()[ToolEditFile].plan,
	}, {
		Name:        ToolWriteFile,
		Description: "Create a text file or replace all of its content. " + where + "To change part of an existing file use " + ToolEditFile + ".",
		InputSchema: kit.InputSchema[WriteFile](),
		Annotations: annotations("Write file"),
		Content:     r.fileContent,
		Prepare:     r.locals()[ToolWriteFile].plan,
	}}
}

type fileChange struct {
	abs, rel string
	exists   bool
	mode     fs.FileMode
	before   string
	after    string
}

func (r *Provider) change(ctx context.Context, caller tool.Caller, args map[string]any) (dirs, fileChange, error) {
	d, err := r.dirs(ctx, caller)
	if err != nil {
		return dirs{}, fileChange{}, err
	}
	raw, _ := args["path"].(string)
	if strings.TrimSpace(raw) == "" {
		return dirs{}, fileChange{}, fmt.Errorf("workspace: argument path must be a non-empty string")
	}
	abs, err := d.box.Canonical(ctx, d.abs(raw))
	if err != nil {
		return dirs{}, fileChange{}, err
	}
	if err := d.box.CheckWrite(d.roots, abs); err != nil {
		return dirs{}, fileChange{}, err
	}
	c := fileChange{abs: abs, rel: d.display(abs), mode: 0o644}
	switch st, err := d.box.Stat(ctx, abs); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return dirs{}, fileChange{}, err
	case !st.Mode().IsRegular():
		return dirs{}, fileChange{}, fmt.Errorf("workspace: %s is not a regular file", c.rel)
	case st.Size() > maxFileBytes:
		return dirs{}, fileChange{}, fmt.Errorf("workspace: %s has %d bytes; files over %d bytes are not edited here", c.rel, st.Size(), maxFileBytes)
	default:
		body, err := sandbox.ReadFile(ctx, d.box, abs, maxFileBytes)
		if err != nil {
			return dirs{}, fileChange{}, err
		}
		if !utf8.Valid(body) {
			return dirs{}, fileChange{}, fmt.Errorf("workspace: %s is not UTF-8 text", c.rel)
		}
		c.exists, c.mode, c.before = true, st.Mode().Perm(), string(body)
	}
	if c.after, err = newContent(c, args); err != nil {
		return dirs{}, fileChange{}, err
	}
	if len(c.after) > maxFileBytes {
		return dirs{}, fileChange{}, fmt.Errorf("workspace: the new %s would have %d bytes; at most %d are written", c.rel, len(c.after), maxFileBytes)
	}
	return d, c, nil
}

func newContent(c fileChange, args map[string]any) (string, error) {
	if _, ok := args["content"]; ok {
		w, _ := kit.DecodeArguments[WriteFile](args)
		return w.Content, nil
	}
	e, _ := kit.DecodeArguments[EditFile](args)
	if !c.exists {
		return "", fmt.Errorf("workspace: %s does not exist; create it with %s", c.rel, ToolWriteFile)
	}
	text := c.before
	for i, edit := range e.Edits {
		n := strings.Count(text, edit.OldText)
		switch {
		case n == 0:
			return "", fmt.Errorf("workspace: edit %d: old_text was not found in %s; read the file again and copy the text exactly", i+1, c.rel)
		case n > 1 && !edit.ReplaceAll:
			return "", fmt.Errorf("workspace: edit %d: old_text appears %d times in %s; add surrounding lines to make it unique, or set replace_all", i+1, n, c.rel)
		}
		text = strings.ReplaceAll(text, edit.OldText, edit.NewText)
	}
	if text == c.before {
		return "", fmt.Errorf("workspace: the edits leave %s unchanged", c.rel)
	}
	return text, nil
}

func (c fileChange) frozen() (FileChange, error) {
	diff, added, removed, err := unifiedDiff(c.rel, c.before, c.after, !c.exists)
	if err != nil {
		return FileChange{}, err
	}
	f := FileChange{Path: c.rel, After: sha(c.after), Diff: diff, Added: added, Removed: removed}
	if c.exists {
		f.Before = sha(c.before)
	}
	return f, nil
}

func (r *Provider) fileContent(ctx context.Context, call tool.Call) (policy.Content, error) {
	caller, args := call.Caller, call.Arguments
	_, c, err := r.change(ctx, caller, args)
	if err != nil {
		return policy.Content{}, err
	}
	f, err := c.frozen()
	if err != nil {
		return policy.Content{}, err
	}
	return policy.Content{Text: f.Diff, Match: strings.ToLower(c.rel), Folded: true}, nil
}

func (r *Provider) prepareFile(ctx context.Context, caller tool.Caller, args map[string]any) (*Prepared, error) {
	d, c, err := r.change(ctx, caller, args)
	if err != nil {
		return nil, err
	}
	f, err := c.frozen()
	if err != nil {
		return nil, err
	}
	return &Prepared{
		Cwd: d.roots[0], SettingsDigest: r.settingsDigest(d), ArgumentsDigest: digest(args),
		Sandboxed: d.box.Enforced(), Files: []FileChange{f},
	}, nil
}

func (r *Provider) runFile(ctx context.Context, caller tool.Caller, args map[string]any, p *Prepared) (kit.Result, error) {
	d, err := r.dirs(ctx, caller)
	if err != nil {
		return kit.Result{}, err
	}
	if err := r.validatePrepared(ctx, d, p, args); err != nil {
		return kit.Result{}, err
	}
	_, c, err := r.change(ctx, caller, args)
	if err != nil {
		return kit.Result{}, fmt.Errorf("%w; the file changed after this edit was prepared, so nothing was written", err)
	}
	want := p.Files[0]
	now, err := c.frozen()
	if err != nil {
		return kit.Result{}, err
	}
	if now.Path != want.Path || now.Before != want.Before || now.After != want.After {
		return kit.Result{}, fmt.Errorf("workspace: %s changed after this edit was prepared, so nothing was written; read it again and redo the edit", want.Path)
	}
	if err := d.box.WriteFile(ctx, c.abs, []byte(c.after), c.mode); err != nil {
		return kit.Result{}, err
	}
	return kit.Result{Output: FileOutput{Path: want.Path, Created: !c.exists, Added: want.Added, Removed: want.Removed}}, nil
}
