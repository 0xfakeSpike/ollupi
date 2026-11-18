package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const defaultMaxOutputBytes = 1 << 20

const drainDelay = 2 * time.Second

func runTag(marker string) string {
	return marker + "=" + randomHex(16)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func runProcess(ctx context.Context, cmd *exec.Cmd, program string, limit time.Duration, maxOutput int, marker string) (Result, error) {
	stdout, err := newCapture(maxOutput)
	if err != nil {
		return Result{}, fmt.Errorf("sandbox: %s: %w", program, err)
	}
	defer stdout.r.Close()
	stderr, err := newCapture(maxOutput)
	if err != nil {
		stdout.w.Close()
		return Result{}, fmt.Errorf("sandbox: %s: %w", program, err)
	}
	defer stderr.r.Close()
	cmd.Stdout, cmd.Stderr = stdout.w, stderr.w
	tag := runTag(marker)
	cmd.Env = append(cmd.Env, tag)
	ownProcessGroup(cmd, tag)
	err = cmd.Start()
	stdout.w.Close()
	stderr.w.Close()
	if err != nil {
		return Result{}, fmt.Errorf("sandbox: start %s: %w", program, err)
	}
	var copies sync.WaitGroup
	copies.Go(stdout.copy)
	copies.Go(stderr.copy)
	err = cmd.Wait()
	killRun(cmd, tag)
	drained := make(chan struct{})
	go func() { copies.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(drainDelay):
		stdout.r.Close()
		stderr.r.Close()
		<-drained
	}
	code, signal := 0, ""
	if ps := cmd.ProcessState; ps != nil {
		code = ps.ExitCode()

		if n, name, ok := exitSignal(ps); ok {
			code, signal = 128+n, name
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Result{}, fmt.Errorf("sandbox: %s timed out after %s and was killed%s", program, limit, outputTail(stdout.buf, stderr.buf))
	}
	if ctx.Err() != nil {
		return Result{}, fmt.Errorf("sandbox: %s was cancelled: %w%s", program, ctx.Err(), outputTail(stdout.buf, stderr.buf))
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || code < 0 {
			return Result{}, fmt.Errorf("sandbox: %s failed: %w%s", program, err, outputTail(stdout.buf, stderr.buf))
		}
	}
	return Result{Stdout: stdout.buf.String(), Stderr: stderr.buf.String(), ExitCode: code, Signal: signal, Truncated: stdout.buf.dropped > 0 || stderr.buf.dropped > 0}, nil
}

type capture struct {
	buf  *cappedBuffer
	r, w *os.File
}

func newCapture(max int) (capture, error) {
	r, w, err := os.Pipe()
	return capture{buf: newCappedBuffer(max), r: r, w: w}, err
}

func (c capture) copy() { _, _ = io.Copy(c.buf, c.r) }

func outputTail(stdout, stderr *cappedBuffer) string {
	var parts []string
	if s := tailOf(stdout); s != "" {
		parts = append(parts, "stdout: "+s)
	}
	if s := tailOf(stderr); s != "" {
		parts = append(parts, "stderr: "+s)
	}
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, "; ")
}

func tailOf(b *cappedBuffer) string {
	const n = 2000
	s := strings.TrimSpace(b.String())
	if len(s) > n {
		s = "…" + s[len(s)-n:]
	}
	return s
}

type cappedBuffer struct {
	max     int
	head    []byte
	tail    []byte
	dropped int64
}

func newCappedBuffer(max int) *cappedBuffer {
	return &cappedBuffer{max: max}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	half := b.max / 2
	if room := half - len(b.head); room > 0 {
		take := min(room, len(p))
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if len(p) == 0 {
		return n, nil
	}
	b.tail = append(b.tail, p...)
	if over := len(b.tail) - (b.max - half); over > 0 {
		b.dropped += int64(over)
		b.tail = append(b.tail[:0], b.tail[over:]...)
	}
	return n, nil
}

func (b *cappedBuffer) String() string {
	if b.dropped == 0 {
		return string(b.head) + string(b.tail)
	}
	return string(b.head) + fmt.Sprintf("\n[… %d bytes omitted …]\n", b.dropped) + string(b.tail)
}

func (s *Sandbox) Start(ctx context.Context, c Command, stderr io.Writer) (Process, error) {
	if c.Program == "" {
		return nil, errors.New("sandbox: incomplete command")
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd, px, tagID, err := s.command(ctx, c)
	if err != nil {
		cancel()
		return nil, err
	}
	stop := func() {
		cancel()
		if px != nil {
			px.close()
		}
		if tagID != "" {
			s.denials.collect(tagID, false)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		stop()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stop()
		return nil, err
	}
	cmd.Stderr = stderr
	cmd.WaitDelay = stopDelay
	tag := runTag(s.runMarker())
	cmd.Env = append(cmd.Env, tag)
	ownProcessGroup(cmd, tag)
	if err := cmd.Start(); err != nil {
		stop()
		return nil, fmt.Errorf("sandbox: start %s: %w", c.Program, err)
	}
	return &process{ReadCloser: stdout, WriteCloser: stdin, cmd: cmd, tag: tag, stop: stop}, nil
}

type process struct {
	io.ReadCloser
	io.WriteCloser
	cmd  *exec.Cmd
	tag  string
	stop func()
	once sync.Once
}

func (p *process) Close() error {
	p.once.Do(func() {
		p.WriteCloser.Close()
		ended := make(chan struct{})
		go func() {
			p.cmd.Wait()
			close(ended)
		}()
		select {
		case <-ended:
		case <-time.After(stopDelay):
		}
		killRun(p.cmd, p.tag)
		p.stop()
		<-ended
	})
	return nil
}

const stopDelay = 2 * time.Second
