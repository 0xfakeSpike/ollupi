package gateway

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/agent/orchestrator"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
)

func TestAnUnknownErrorStaysOnTheServer(t *testing.T) {
	internal := serverError(errors.New("no such table at /srv/shard/agent.db"))
	if internal.Code != jsonrpc.CodeServerError || strings.Contains(internal.Message, "/srv") {
		t.Errorf("an unknown error: %+v", internal)
	}
	known := serverError(fmt.Errorf("agent: %w: /nowhere is unavailable", orchestrator.ErrInvalidPath))
	if known.Code != jsonrpc.CodeInvalidParams || !strings.Contains(known.Message, "/nowhere") {
		t.Errorf("a known error: %+v", known)
	}
}
