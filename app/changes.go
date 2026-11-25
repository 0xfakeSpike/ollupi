package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
)

// A workspace's uncommitted changes as git sees them, and what the person
// does with them: stage, unstage, discard, commit. git runs where the
// workspace is, in its environment, as the person: with full access, as
// their own commands in a terminal do, so that their identity, signing
// keys and hooks work as they do outside ollupi. ollupi changes files
// only through commands and records no edits of its own, so the repository
// is where a turn's work shows. Renames show as a deletion and an
// addition, as they do to git without rename detection.
const (
	// DiffBytes is as much of a diff as a person reads in a panel.
	DiffBytes  = 512 << 10
	gitTimeout = 30 * time.Second
	// emptyTree is what a repository without commits is compared with.
	emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
)

var ErrNotARepository = errors.New("the workspace is not in a git repository")

type ChangeKind string

const (
	ChangeModified  ChangeKind = "modified"
	ChangeAdded     ChangeKind = "added"
	ChangeDeleted   ChangeKind = "deleted"
	ChangeUntracked ChangeKind = "untracked"
)

var ChangeKinds = []ChangeKind{ChangeModified, ChangeAdded, ChangeDeleted, ChangeUntracked}

// Staged is how much of a file's change the index holds: all of it, part
// of it (more changed in the working tree since), or none.
type Staged string

const (
	StagedAll  Staged = "all"
	StagedPart Staged = "part"
	StagedNone Staged = "none"
)

var StagedValues = []Staged{StagedAll, StagedPart, StagedNone}

// FileChange is a file that differs from the last commit, by its path
// relative to the workspace root. Its line counts are nil for a binary or
// untracked file.
type FileChange struct {
	Path    string
	Kind    ChangeKind
	Staged  Staged
	Added   *int
	Removed *int
}

// Head is where the repository's HEAD is: on a branch, which may have no
// commit yet, or detached at a commit, by its short hash.
type Head struct {
	Branch   string
	Detached string
}

// Changes are a workspace's uncommitted changes and where its HEAD is;
// Repo is false outside a git repository.
type Changes struct {
	Repo  bool
	Head  Head
	Files []FileChange
}

type FileDiff struct {
	Text      string
	Truncated bool
}

type Commit struct {
	Hash    string
	Subject string
}

// repo is a workspace in its repository: where in it the workspace is,
// and what the workspace is compared with.
type repo struct {
	place
	prefix string
	// head is whether the repository has a commit.
	head bool
	base string
}

func (a *App) repo(ctx context.Context, user, workspace string) (repo, error) {
	p, err := a.place(ctx, user, workspace)
	if err != nil {
		return repo{}, err
	}
	r := repo{place: p}
	prefix, err := r.git(ctx, "rev-parse", "--show-prefix")
	if err != nil {
		return repo{}, ErrNotARepository
	}
	r.prefix = strings.TrimSpace(prefix)
	_, err = r.git(ctx, "rev-parse", "--verify", "--quiet", "HEAD")
	r.head = err == nil
	r.base = emptyTree
	if r.head {
		r.base = "HEAD"
	}
	return r, nil
}

// Changes are a workspace's uncommitted changes.
func (a *App) Changes(ctx context.Context, user, workspace string) (Changes, error) {
	r, err := a.repo(ctx, user, workspace)
	if errors.Is(err, ErrNotARepository) {
		return Changes{Files: []FileChange{}}, nil
	}
	if err != nil {
		return Changes{}, err
	}
	status, err := r.git(ctx, "status", "--porcelain=v1", "-z", "--no-renames", "--untracked-files=all", "--", ".")
	if err != nil {
		return Changes{}, err
	}
	counts, err := r.git(ctx, "diff", r.base, "--numstat", "-z", "--no-renames", "--", ".")
	if err != nil {
		return Changes{}, err
	}
	type lines struct{ added, removed int }
	numbers := map[string]lines{}
	for entry := range strings.SplitSeq(counts, "\x00") {
		parts := strings.SplitN(entry, "\t", 3)
		// A binary file's counts are "-".
		if len(parts) != 3 || parts[0] == "-" {
			continue
		}
		added, aerr := strconv.Atoi(parts[0])
		removed, rerr := strconv.Atoi(parts[1])
		if aerr == nil && rerr == nil {
			numbers[parts[2]] = lines{added, removed}
		}
	}
	c := Changes{Repo: true, Files: []FileChange{}}
	for entry := range strings.SplitSeq(status, "\x00") {
		if len(entry) < 4 {
			continue
		}
		xy, path := entry[:2], entry[3:]
		kind, ok := changeKind(xy)
		if !ok {
			continue
		}
		f := FileChange{Path: strings.TrimPrefix(path, r.prefix), Kind: kind, Staged: stagedOf(xy)}
		if n, ok := numbers[path]; ok {
			f.Added, f.Removed = &n.added, &n.removed
		}
		c.Files = append(c.Files, f)
	}
	if c.Head, err = r.headOf(ctx); err != nil {
		return Changes{}, err
	}
	return c, nil
}

// headOf is the branch HEAD is on, an unborn one too; otherwise the
// commit it is detached at.
func (r repo) headOf(ctx context.Context) (Head, error) {
	branch, err := r.gitOK(ctx, []int{0, 1}, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return Head{}, err
	}
	if b := strings.TrimSpace(branch); b != "" {
		return Head{Branch: b}, nil
	}
	hash, err := r.git(ctx, "rev-parse", "--short", "HEAD")
	return Head{Detached: strings.TrimSpace(hash)}, err
}

// Diff is a file's diff against the last commit; an untracked one's is
// against nothing.
func (a *App) Diff(ctx context.Context, user, workspace, rel string, untracked bool) (FileDiff, error) {
	r, err := a.repo(ctx, user, workspace)
	if err != nil {
		return FileDiff{}, err
	}
	path, err := literal(rel)
	if err != nil {
		return FileDiff{}, err
	}
	var text string
	var truncated bool
	if untracked {
		// git reads an untracked file outside its index, so the path must
		// stay in the workspace.
		if _, err := r.path(ctx, rel); err != nil {
			return FileDiff{}, err
		}
		text, truncated, err = r.gitRaw(ctx, []int{0, 1}, "diff", "--no-index", "--no-color", "--", "/dev/null", filepath.FromSlash(rel))
	} else {
		text, truncated, err = r.gitRaw(ctx, []int{0}, "diff", r.base, "--no-color", "--no-renames", "--", path)
	}
	if err != nil {
		return FileDiff{}, err
	}
	if len(text) > DiffBytes {
		text, truncated = strings.ToValidUTF8(text[:DiffBytes], ""), true
	}
	return FileDiff{Text: text, Truncated: truncated}, nil
}

// Stage stages the files' changes, deletions included.
func (a *App) Stage(ctx context.Context, user, workspace string, paths []string) error {
	r, err := a.repo(ctx, user, workspace)
	if err != nil {
		return err
	}
	literals, err := literals(paths)
	if err != nil {
		return err
	}
	_, err = r.git(ctx, append([]string{"add", "-A", "--"}, literals...)...)
	return err
}

// Unstage takes the files' changes out of the index, keeping them in the
// working tree. Before the first commit nothing is in HEAD to go back to,
// so the files leave the index.
func (a *App) Unstage(ctx context.Context, user, workspace string, paths []string) error {
	r, err := a.repo(ctx, user, workspace)
	if err != nil {
		return err
	}
	literals, err := literals(paths)
	if err != nil {
		return err
	}
	args := []string{"rm", "--cached", "-q", "-r", "--"}
	if r.head {
		args = []string{"restore", "--staged", "--"}
	}
	_, err = r.git(ctx, append(args, literals...)...)
	return err
}

// Discard puts a file back as the last commit has it, in the index and
// the working tree. A file no commit holds has nothing to go back to: it
// leaves the index and goes to the trash of the workspace's machine, where
// the person can still find it.
func (a *App) Discard(ctx context.Context, user, workspace, rel string, kind ChangeKind) error {
	r, err := a.repo(ctx, user, workspace)
	if err != nil {
		return err
	}
	path, err := literal(rel)
	if err != nil {
		return err
	}
	switch kind {
	case ChangeModified, ChangeDeleted:
		_, err := r.git(ctx, "restore", "--source=HEAD", "--staged", "--worktree", "--", path)
		return err
	case ChangeAdded:
		if _, err := r.git(ctx, "rm", "--cached", "-q", "--", path); err != nil {
			return err
		}
	case ChangeUntracked:
	default:
		return fmt.Errorf("%w: a change is one of %q, not %q", ErrInvalidPath, ChangeKinds, kind)
	}
	abs, err := r.path(ctx, rel)
	if err != nil {
		return err
	}
	return r.box.Trash(ctx, abs)
}

// CommitChanges commits what is staged with the message, as the person's
// git identity, hooks included.
func (a *App) CommitChanges(ctx context.Context, user, workspace, message string) (Commit, error) {
	if strings.TrimSpace(message) == "" {
		return Commit{}, fmt.Errorf("%w: a commit needs a message", ErrInvalidMessage)
	}
	r, err := a.repo(ctx, user, workspace)
	if err != nil {
		return Commit{}, err
	}
	if _, err := r.git(ctx, "commit", "-q", "-m", message); err != nil {
		return Commit{}, err
	}
	out, err := r.git(ctx, "log", "-1", "--format=%h%x00%s")
	if err != nil {
		return Commit{}, err
	}
	hash, subject, _ := strings.Cut(strings.TrimSpace(out), "\x00")
	return Commit{Hash: hash, Subject: subject}, nil
}

func (r repo) git(ctx context.Context, args ...string) (string, error) {
	return r.gitOK(ctx, []int{0}, args...)
}

func (r repo) gitOK(ctx context.Context, ok []int, args ...string) (string, error) {
	out, truncated, err := r.gitRaw(ctx, ok, args...)
	if err == nil && truncated {
		err = fmt.Errorf("git %s said more than can be read", args[0])
	}
	return out, err
}

// gitRaw runs git in the workspace's folder, as the person. No optional
// locks: the agent may be running git in the same repository.
func (r repo) gitRaw(ctx context.Context, ok []int, args ...string) (string, bool, error) {
	res, err := r.box.Run(ctx, sandbox.Command{Program: "git", Args: append([]string{"-c", "core.quotePath=off"}, args...), Cwd: r.root,
		Env: map[string]string{"GIT_OPTIONAL_LOCKS": "0"}, Timeout: gitTimeout, FullAccess: true})
	if err != nil {
		return "", false, fmt.Errorf("git %s: %w", args[0], err)
	}
	if !slices.Contains(ok, res.ExitCode) {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = fmt.Sprintf("exit status %d", res.ExitCode)
		}
		return "", false, fmt.Errorf("git %s: %s", args[0], msg)
	}
	return res.Stdout, res.Truncated, nil
}

// literal is a path relative to the workspace as git takes it from the
// workspace's folder: one that leaves the workspace or is absolute is
// refused, and git reads it literally, not as a pattern.
func literal(rel string) (string, error) {
	p := filepath.Clean(filepath.FromSlash(rel))
	if rel == "" || p == "." || !filepath.IsLocal(p) {
		return "", fmt.Errorf("%w: %q is not a path in the workspace", ErrInvalidPath, rel)
	}
	return ":(literal)" + p, nil
}

func literals(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("%w: no files named", ErrInvalidPath)
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		l, err := literal(p)
		if err != nil {
			return nil, err
		}
		out[i] = l
	}
	return out, nil
}

// stagedOf is how much of a file's change the index holds: X is the index
// against HEAD, Y the working tree against the index.
func stagedOf(xy string) Staged {
	switch {
	case xy[0] == ' ' || xy[0] == '?':
		return StagedNone
	case xy[1] == ' ':
		return StagedAll
	}
	return StagedPart
}

// changeKind is the file against the last commit, from both of
// porcelain's columns: the index (X) and the working tree (Y). A file added
// to the index and then deleted is in neither, so it has not changed.
func changeKind(xy string) (ChangeKind, bool) {
	if xy == "??" {
		return ChangeUntracked, true
	}
	x, y := xy[0], xy[1]
	inHead := x != 'A'
	inTree := y != 'D' && !(x == 'D' && y == ' ')
	switch {
	case !inHead && inTree:
		return ChangeAdded, true
	case !inHead:
		return "", false
	case inTree:
		return ChangeModified, true
	}
	return ChangeDeleted, true
}
