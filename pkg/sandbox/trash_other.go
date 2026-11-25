//go:build !darwin

package sandbox

func trash(path, home string) error { return freedesktopTrash(path, home) }
