package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxMessageBytes = 32 << 20

var errMessageTooLarge = fmt.Errorf("mcp: the server sent a message of more than %d bytes", maxMessageBytes)

type stdioMessages struct {
	io.Closer
	r    *bufio.Reader
	next []byte
	err  error
}

func newStdioMessages(rc io.ReadCloser) *stdioMessages {
	return &stdioMessages{Closer: rc, r: bufio.NewReader(rc)}
}

func (m *stdioMessages) Read(p []byte) (int, error) {
	for len(m.next) == 0 {
		if m.err != nil {
			return 0, m.err
		}
		var line []byte
		line, m.err = readLine(m.r)
		if t := bytes.TrimSpace(line); len(t) > 0 && !json.Valid(t) && !errors.Is(m.err, io.EOF) {
			m.err = errors.New("mcp: the server wrote a line that is not one JSON message")
			return 0, m.err
		}
		m.next = line
	}
	n := copy(p, m.next)
	m.next = m.next[n:]
	return n, nil
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > maxMessageBytes {
			return nil, errMessageTooLarge
		}
		line = append(line, chunk...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

func limitBody(res *http.Response) io.ReadCloser {
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		return &eventLimit{ReadCloser: res.Body}
	}
	return http.MaxBytesReader(nil, res.Body, maxMessageBytes)
}

type eventLimit struct {
	io.ReadCloser
	run      int
	lineFeed bool
}

func (e *eventLimit) Read(p []byte) (int, error) {
	n, err := e.ReadCloser.Read(p)
	for _, b := range p[:n] {
		switch {
		case b == '\r':
		case b == '\n' && e.lineFeed:
			e.run = 0
		default:
			e.run++
		}
		if b != '\r' {
			e.lineFeed = b == '\n'
		}
		if e.run > maxMessageBytes {
			return 0, errMessageTooLarge
		}
	}
	return n, err
}
