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
	stdout := newCappedBuffer(maxOutput)
	stderr := newCappedBuffer(maxOutput)

	outR, outW, err := os.Pipe()
	if err != nil {
		return Result{}, fmt.Errorf("sandbox: %s: %w", program, err)
	}
	defer outR.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		outW.Close()
		return Result{}, fmt.Errorf("sandbox: %s: %w", program, err)
	}
	defer errR.Close()
	cmd.Stdout, cmd.Stderr = outW, errW
	tag := runTag(marker)
	cmd.Env = append(cmd.Env, tag)
	ownProcessGroup(cmd, tag)
	err = cmd.Start()

	outW.Close()
	errW.Close()
	if err != nil {
		return Result{}, fmt.Errorf("sandbox: start %s: %w", program, err)
	}
	var copies sync.WaitGroup
	copies.Add(2)
	go func() {
		defer copies.Done()
		_, _ = io.Copy(stdout, outR)
	}()
	go func() {
		defer copies.Done()
		_, _ = io.Copy(stderr, errR)
	}()
	err = cmd.Wait()
	killRun(cmd, tag)
	drained := make(chan struct{})
	go func() { copies.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(drainDelay):

		outR.Close()
		errR.Close()
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
		return Result{}, fmt.Errorf("sandbox: %s timed out after %s and was killed%s", program, limit, outputTail(stdout, stderr))
	}
	if ctx.Err() != nil {
		return Result{}, fmt.Errorf("sandbox: %s was cancelled: %w%s", program, ctx.Err(), outputTail(stdout, stderr))
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || code < 0 {
			return Result{}, fmt.Errorf("sandbox: %s failed: %w%s", program, err, outputTail(stdout, stderr))
		}
	}
	return Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: code, Signal: signal, Truncated: stdout.dropped > 0 || stderr.dropped > 0}, nil
}

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
	if max <= 0 {
		max = defaultMaxOutputBytes
	}
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
