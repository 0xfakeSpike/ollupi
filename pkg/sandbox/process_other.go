//go:build !unix

package sandbox

import (
	"os"
	"os/exec"
)

func ownProcessGroup(*exec.Cmd, string) {}
func killRun(*exec.Cmd, string)         {}

func exitSignal(*os.ProcessState) (int, string, bool) { return 0, "", false }
