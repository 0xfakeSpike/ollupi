package gateway

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"path"
	"strconv"

	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/store"
)

// A workspace's files and its git changes as a person sees them beside the
// conversation, read and changed where the workspace is, in its
// environment; and the folders of an environment's machine, to choose a
// workspace's among. Paths of a workspace are relative to its root,
// slash-separated.

type WorkspacePathParams struct {
	WorkspaceID string `json:"workspaceId"`
	// Path is relative to the workspace root; empty is the root.
	Path string `json:"path,omitempty"`
}

type FileEntryRow struct {
	Name string       `json:"name"`
	Kind app.FileKind `json:"kind"`
}

type WorkspaceFilesResult struct {
	Data []FileEntryRow `json:"data"`
}

// FilePreviewRow is a file as a preview shows it: the start of a text
// file, or that it is an image or a PDF, whose bytes the workspaceFile
// path serves, or none of these.
type FilePreviewRow struct {
	Kind      app.PreviewKind `json:"kind"`
	Size      int64           `json:"size"`
	Text      string          `json:"text,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
	MimeType  string          `json:"mimeType,omitempty"`
}

type WorkspaceStatResult struct {
	// Kind is absent when the path is neither a file nor a folder, is
	// missing, or leads out of the workspace.
	Kind app.FileKind `json:"kind,omitempty"`
}

type WorkspaceIDParams struct {
	WorkspaceID string `json:"workspaceId"`
}

type HeadRow struct {
	Branch   string `json:"branch,omitempty"`
	Detached string `json:"detached,omitempty"`
}

type FileChangeRow struct {
	Path    string         `json:"path"`
	Kind    app.ChangeKind `json:"kind"`
	Staged  app.Staged     `json:"staged"`
	Added   *int           `json:"added,omitempty"`
	Removed *int           `json:"removed,omitempty"`
}

// ChangesResult is a workspace's uncommitted changes; repo is false, and
// the rest absent, outside a git repository.
type ChangesResult struct {
	Repo  bool            `json:"repo"`
	Head  *HeadRow        `json:"head,omitempty"`
	Files []FileChangeRow `json:"files"`
}

type WorkspaceDiffParams struct {
	WorkspaceID string `json:"workspaceId"`
	Path        string `json:"path"`
	Untracked   bool   `json:"untracked,omitempty"`
}

type FileDiffResult struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

type WorkspacePathsParams struct {
	WorkspaceID string   `json:"workspaceId"`
	Paths       []string `json:"paths"`
}

type WorkspaceDiscardParams struct {
	WorkspaceID string         `json:"workspaceId"`
	Path        string         `json:"path"`
	Kind        app.ChangeKind `json:"kind"`
}

type WorkspaceCommitParams struct {
	WorkspaceID string `json:"workspaceId"`
	Message     string `json:"message"`
}

type CommitResult struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
}

type EnvironmentFoldersParams struct {
	// EnvironmentID omitted is the user's first.
	EnvironmentID string `json:"environmentId,omitempty"`
	// Path is absolute; omitted, the environment's own folder.
	Path string `json:"path,omitempty"`
}

type EnvironmentFoldersResult struct {
	Path string `json:"path"`
	// Parent is absent at the root.
	Parent  string   `json:"parent,omitempty"`
	Folders []string `json:"folders"`
}

type EnvironmentMakeFolderParams struct {
	EnvironmentID string `json:"environmentId,omitempty"`
	Parent        string `json:"parent"`
	Name          string `json:"name"`
}

type EnvironmentMakeFolderResult struct {
	Path string `json:"path"`
}

func (c *conn) workspaceListFiles(ctx context.Context, p WorkspacePathParams) (WorkspaceFilesResult, error) {
	entries, err := c.s.app.ListFiles(ctx, c.user, p.WorkspaceID, p.Path)
	return WorkspaceFilesResult{Data: each(entries, func(e app.FileEntry) FileEntryRow { return FileEntryRow{Name: e.Name, Kind: e.Kind} })}, err
}

func (c *conn) workspaceReadFile(ctx context.Context, p WorkspacePathParams) (FilePreviewRow, error) {
	f, err := c.s.app.PreviewFile(ctx, c.user, p.WorkspaceID, p.Path)
	return FilePreviewRow{Kind: f.Kind, Size: f.Size, Text: f.Text, Truncated: f.Truncated, MimeType: f.MimeType}, err
}

func (c *conn) workspaceStatFile(ctx context.Context, p WorkspacePathParams) (WorkspaceStatResult, error) {
	kind, err := c.s.app.StatFile(ctx, c.user, p.WorkspaceID, p.Path)
	return WorkspaceStatResult{Kind: kind}, err
}

func (c *conn) workspaceChanges(ctx context.Context, p WorkspaceIDParams) (ChangesResult, error) {
	ch, err := c.s.app.Changes(ctx, c.user, p.WorkspaceID)
	if err != nil {
		return ChangesResult{}, err
	}
	out := ChangesResult{Repo: ch.Repo, Files: each(ch.Files, func(f app.FileChange) FileChangeRow {
		return FileChangeRow{Path: f.Path, Kind: f.Kind, Staged: f.Staged, Added: f.Added, Removed: f.Removed}
	})}
	if ch.Repo {
		out.Head = &HeadRow{Branch: ch.Head.Branch, Detached: ch.Head.Detached}
	}
	return out, nil
}

func (c *conn) workspaceDiff(ctx context.Context, p WorkspaceDiffParams) (FileDiffResult, error) {
	d, err := c.s.app.Diff(ctx, c.user, p.WorkspaceID, p.Path, p.Untracked)
	return FileDiffResult{Text: d.Text, Truncated: d.Truncated}, err
}

func (c *conn) workspaceStage(ctx context.Context, p WorkspacePathsParams) (Empty, error) {
	return Empty{}, c.s.app.Stage(ctx, c.user, p.WorkspaceID, p.Paths)
}

func (c *conn) workspaceUnstage(ctx context.Context, p WorkspacePathsParams) (Empty, error) {
	return Empty{}, c.s.app.Unstage(ctx, c.user, p.WorkspaceID, p.Paths)
}

func (c *conn) workspaceDiscard(ctx context.Context, p WorkspaceDiscardParams) (Empty, error) {
	return Empty{}, c.s.app.Discard(ctx, c.user, p.WorkspaceID, p.Path, p.Kind)
}

func (c *conn) workspaceCommit(ctx context.Context, p WorkspaceCommitParams) (CommitResult, error) {
	commit, err := c.s.app.CommitChanges(ctx, c.user, p.WorkspaceID, p.Message)
	return CommitResult{Hash: commit.Hash, Subject: commit.Subject}, err
}

func (c *conn) environmentListFolders(ctx context.Context, p EnvironmentFoldersParams) (EnvironmentFoldersResult, error) {
	f, err := c.s.app.ListFolders(ctx, c.user, p.EnvironmentID, p.Path)
	return EnvironmentFoldersResult{Path: f.Path, Parent: f.Parent, Folders: f.Folders}, err
}

func (c *conn) environmentMakeFolder(ctx context.Context, p EnvironmentMakeFolderParams) (EnvironmentMakeFolderResult, error) {
	path, err := c.s.app.MakeFolder(ctx, c.user, p.EnvironmentID, p.Parent, p.Name)
	return EnvironmentMakeFolderResult{Path: path}, err
}

// WorkspaceFilePath serves a file of a workspace whole, up to
// app.FileBytes, for a preview to show an image or a PDF by: path is
// relative to the workspace root, each name escaped.
const WorkspaceFilePath = "/workspaces/{workspace}/files/{path...}"

func (s *Server) workspaceFile(w http.ResponseWriter, r *http.Request) {
	id, ok := s.authorize(w, r)
	if !ok || !s.serves(w, r, id.User) {
		return
	}
	rel := r.PathValue("path")
	data, kind, err := s.app.OpenFile(r.Context(), id.User, r.PathValue("workspace"), rel)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, app.ErrInvalidPath):
		http.Error(w, "no such file in the workspace", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, "the file could not be read now", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", kind)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": path.Base(rel)}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.Write(data)
}
