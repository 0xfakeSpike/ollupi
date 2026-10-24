package sandbox

import (
	"bytes"
	"encoding/binary"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const zombie = 5

func killTagged(tag string, leader int) {
	self := int32(os.Getpid())
	for range 200 {
		procs, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
		if err != nil {
			return
		}
		children := map[int32][]int32{}
		groups := map[int32][]int32{}
		var queue []int32
		for _, p := range procs {
			pid := p.Proc.P_pid
			if pid <= 1 || pid == self || p.Proc.P_stat == zombie {
				continue
			}
			children[p.Eproc.Ppid] = append(children[p.Eproc.Ppid], pid)
			groups[p.Eproc.Pgid] = append(groups[p.Eproc.Pgid], pid)
			if hasEnv(pid, tag) {
				queue = append(queue, pid)
			}
		}
		queue = append(queue, groups[int32(leader)]...)
		found := map[int32]bool{}
		for len(queue) > 0 {
			pid := queue[0]
			queue = queue[1:]
			if found[pid] || pid == self {
				continue
			}
			found[pid] = true
			queue = append(append(queue, children[pid]...), groups[pid]...)
		}
		if len(found) == 0 {
			return
		}
		for pid := range found {
			_ = unix.Kill(int(pid), unix.SIGSTOP)
		}
		for pid := range found {
			_ = unix.Kill(int(pid), unix.SIGKILL)
		}
		time.Sleep(time.Millisecond)
	}
}

func hasEnv(pid int32, entry string) bool {
	buf, err := unix.SysctlRaw("kern.procargs2", int(pid))
	if err != nil || len(buf) < 4 {
		return false
	}
	argc := int(binary.NativeEndian.Uint32(buf))
	rest := buf[4:]
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return false
	}
	rest = bytes.TrimLeft(rest[i:], "\x00")
	for ; argc > 0; argc-- {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return false
		}
		rest = rest[i+1:]
	}
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, 0)
		if i <= 0 {
			return false
		}
		if string(rest[:i]) == entry {
			return true
		}
		rest = rest[i+1:]
	}
	return false
}
