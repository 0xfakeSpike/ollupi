package app

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
)

// A workspace's files as a person browses them beside the conversation:
// read where the workspace is, in its environment, never outside its root.
const (
	// TextPreviewBytes is as much of a text file as a preview holds.
	TextPreviewBytes = 256 << 10
	// FileBytes is the largest file sent whole, as an image or a PDF is.
	FileBytes = 50 << 20
	// sniffBytes are read for a NUL, which makes a file binary, as git
	// decides.
	sniffBytes = 8000
)

// hiddenNames are git's own data and Finder's; neither is the work.
var hiddenNames = []string{".git", ".DS_Store"}

type FileKind string

const (
	FileKindFile   FileKind = "file"
	FileKindFolder FileKind = "folder"
)

var FileKinds = []FileKind{FileKindFile, FileKindFolder}

type FileEntry struct {
	Name string
	Kind FileKind
}

type PreviewKind string

const (
	PreviewText   PreviewKind = "text"
	PreviewImage  PreviewKind = "image"
	PreviewPDF    PreviewKind = "pdf"
	PreviewBinary PreviewKind = "binary"
)

var PreviewKinds = []PreviewKind{PreviewText, PreviewImage, PreviewPDF, PreviewBinary}

// Preview is a file as a preview shows it: the start of its text, or that
// it is an image or a PDF, whose bytes are read whole (ReadFile), or that
// it is none of these. Size is the file's.
type Preview struct {
	Kind      PreviewKind
	Size      int64
	Text      string
	Truncated bool
	MimeType  string
}

var previewImages = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml",
}

// ListFiles is a folder of a workspace, folders first, then by name. A
// symlink is what it points to; one that points nowhere is left out.
func (a *App) ListFiles(ctx context.Context, user, workspace, rel string) ([]FileEntry, error) {
	p, err := a.place(ctx, user, workspace)
	if err != nil {
		return nil, err
	}
	dir, err := p.path(ctx, rel)
	if err != nil {
		return nil, err
	}
	entries, err := p.box.ReadDir(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	out := []FileEntry{}
	for _, e := range entries {
		if slices.Contains(hiddenNames, e.Name()) {
			continue
		}
		folder := e.IsDir()
		if e.Type()&fs.ModeSymlink != 0 {
			st, err := p.box.Stat(ctx, filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			folder = st.IsDir()
		}
		kind := FileKindFile
		if folder {
			kind = FileKindFolder
		}
		out = append(out, FileEntry{Name: e.Name(), Kind: kind})
	}
	slices.SortFunc(out, func(x, y FileEntry) int {
		if x.Kind != y.Kind {
			if x.Kind == FileKindFolder {
				return -1
			}
			return 1
		}
		return cmp.Or(naturalCompare(strings.ToLower(x.Name), strings.ToLower(y.Name)), strings.Compare(x.Name, y.Name))
	})
	return out, nil
}

// StatFile is whether a path of a workspace is a file or a folder; empty
// when it is neither, is missing, or leads out of the workspace.
func (a *App) StatFile(ctx context.Context, user, workspace, rel string) (FileKind, error) {
	p, err := a.place(ctx, user, workspace)
	if err != nil {
		return "", err
	}
	path, err := p.path(ctx, rel)
	if err != nil {
		return "", nil
	}
	st, err := p.box.Stat(ctx, path)
	switch {
	case err != nil:
		return "", nil
	case st.IsDir():
		return FileKindFolder, nil
	case st.Mode().IsRegular():
		return FileKindFile, nil
	}
	return "", nil
}

// PreviewFile is a file of a workspace as a preview shows it.
func (a *App) PreviewFile(ctx context.Context, user, workspace, rel string) (Preview, error) {
	p, err := a.place(ctx, user, workspace)
	if err != nil {
		return Preview{}, err
	}
	path, err := p.path(ctx, rel)
	if err != nil {
		return Preview{}, err
	}
	st, err := p.box.Stat(ctx, path)
	if err != nil {
		return Preview{}, fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	if !st.Mode().IsRegular() {
		return Preview{}, fmt.Errorf("%w: %q is not a file", ErrInvalidPath, rel)
	}
	size, ext := st.Size(), strings.ToLower(filepath.Ext(path))
	if ext == ".pdf" {
		if size > FileBytes {
			return Preview{Kind: PreviewBinary, Size: size}, nil
		}
		return Preview{Kind: PreviewPDF, Size: size, MimeType: "application/pdf"}, nil
	}
	if mime, ok := previewImages[ext]; ok {
		if size > FileBytes {
			return Preview{Kind: PreviewBinary, Size: size}, nil
		}
		return Preview{Kind: PreviewImage, Size: size, MimeType: mime}, nil
	}
	f, err := p.box.Open(ctx, path)
	if err != nil {
		return Preview{}, fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	defer f.Close()
	head, err := io.ReadAll(io.LimitReader(f, TextPreviewBytes))
	if err != nil {
		return Preview{}, err
	}
	if bytes.IndexByte(head[:min(len(head), sniffBytes)], 0) >= 0 {
		return Preview{Kind: PreviewBinary, Size: size}, nil
	}
	truncated := size > TextPreviewBytes
	if truncated {
		// The cut may fall inside a character; the text ends before it.
		for len(head) > 0 && !utf8.Valid(head) {
			head = head[:len(head)-1]
		}
	}
	return Preview{Kind: PreviewText, Size: size, Text: strings.ToValidUTF8(string(head), "�"), Truncated: truncated}, nil
}

// OpenFile reads a whole file of a workspace, up to FileBytes, with the
// type its name gives it.
func (a *App) OpenFile(ctx context.Context, user, workspace, rel string) (data []byte, mime string, err error) {
	p, err := a.place(ctx, user, workspace)
	if err != nil {
		return nil, "", err
	}
	path, err := p.path(ctx, rel)
	if err != nil {
		return nil, "", err
	}
	data, err = sandbox.ReadFile(ctx, p.box, path, FileBytes)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".pdf" {
		return data, "application/pdf", nil
	}
	if mime, ok := previewImages[ext]; ok {
		return data, mime, nil
	}
	return data, "application/octet-stream", nil
}

// naturalCompare orders names as Finder does: a run of digits by its
// number, so file2 comes before file10.
func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		da, db := digits(a), digits(b)
		if da > 0 && db > 0 {
			na, nb := strings.TrimLeft(a[:da], "0"), strings.TrimLeft(b[:db], "0")
			if c := cmp.Or(cmp.Compare(len(na), len(nb)), strings.Compare(na, nb)); c != 0 {
				return c
			}
			a, b = a[da:], b[db:]
			continue
		}
		ra, sa := utf8.DecodeRuneInString(a)
		rb, sb := utf8.DecodeRuneInString(b)
		if ra != rb {
			return cmp.Compare(ra, rb)
		}
		a, b = a[sa:], b[sb:]
	}
	return cmp.Compare(len(a), len(b))
}

func digits(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}
