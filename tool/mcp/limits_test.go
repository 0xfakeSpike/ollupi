package mcp

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestStdioMessagesPassWholeMessages(t *testing.T) {
	in := "{\"jsonrpc\":\"2.0\",\"id\":1}\n{\"jsonrpc\":\"2.0\",\"id\":2}\n"
	got, err := io.ReadAll(newStdioMessages(io.NopCloser(strings.NewReader(in))))
	if err != nil || string(got) != in {
		t.Fatalf("read %q, %v", got, err)
	}
	if _, err := io.ReadAll(newStdioMessages(io.NopCloser(strings.NewReader("{\n\"id\": 1}\n")))); err == nil {
		t.Error("a message split over lines passes")
	}
	huge := append(bytes.Repeat([]byte("a"), maxMessageBytes+1), '\n')
	if _, err := io.ReadAll(newStdioMessages(io.NopCloser(bytes.NewReader(huge)))); !errors.Is(err, errMessageTooLarge) {
		t.Errorf("a line over the limit: %v", err)
	}
}

func TestAStreamFailsAtAnEventOverTheLimit(t *testing.T) {
	stream := func(body []byte) io.ReadCloser {
		res := &http.Response{Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(bytes.NewReader(body))}
		return limitBody(res)
	}
	small := bytes.Repeat([]byte("data: {}\r\n\r\n"), maxMessageBytes/8)
	if _, err := io.ReadAll(stream(small)); err != nil {
		t.Errorf("many small events: %v", err)
	}
	big := append([]byte("data: "), bytes.Repeat([]byte("a\ndata: "), maxMessageBytes/7)...)
	if _, err := io.ReadAll(stream(big)); !errors.Is(err, errMessageTooLarge) {
		t.Errorf("an event over the limit: %v", err)
	}
	res := &http.Response{Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(make([]byte, maxMessageBytes+1)))}
	if _, err := io.ReadAll(limitBody(res)); err == nil {
		t.Error("a body over the limit is read")
	}
}
