package jsonrpc

import (
	"encoding/json"
	"testing"
)

func TestDecodeParamsRefusesWhatTheMethodDoesNotTake(t *testing.T) {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	if err := DecodeParams(json.RawMessage(`{"threadId":"t"}`), &p); err != nil || p.ThreadID != "t" {
		t.Fatalf("known fields: %v %+v", err, p)
	}
	if err := DecodeParams(nil, &p); err != nil {
		t.Errorf("no params: %v", err)
	}
	for _, raw := range []string{`{"threadId":"t","userId":"someone else"}`, `{"threadId":1}`, `[`} {
		if err := DecodeParams(json.RawMessage(raw), &p); err == nil || err.Code != CodeInvalidParams {
			t.Errorf("%s: %v", raw, err)
		}
	}
}
