//go:build darwin && !cgo

package sandbox

import "errors"

// trash needs Foundation, which a build without cgo cannot call.
func trash(string, string) error {
	return errors.New("sandbox: moving to the Trash needs a build with cgo")
}
