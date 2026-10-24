//go:build unix

package sandbox

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

func ownProcessGroup(cmd *exec.Cmd, tag string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		killRun(cmd, tag)
		return nil
	}
}

func killRun(cmd *exec.Cmd, tag string) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	killTagged(tag, cmd.Process.Pid)
}

func exitSignal(ps *os.ProcessState) (int, string, bool) {
	ws, ok := ps.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return 0, "", false
	}
	sig := ws.Signal()
	name := unix.SignalName(sig)
	if name == "" {
		name = sig.String()
	}
	return int(sig), name, true
}
