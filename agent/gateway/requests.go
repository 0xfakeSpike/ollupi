package gateway

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent/service"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
	"github.com/coder/websocket"
)

// A client that cannot be told what waits on it would wait without knowing, so it is dropped and asked again when it
// comes back.
func (c *conn) requests(pending []service.Request, err error) {
	if errors.Is(err, db.ErrNotFound) {
		return
	}
	if err != nil {
		go c.ws.Close(websocket.StatusInternalError, "pending requests could not be read")
		return
	}
	for _, r := range pending {
		c.request(r.Thread.ID, r.Item)
	}
}

func (c *conn) request(threadID string, it db.ItemRow) {
	c.mu.Lock()
	if c.asked[it.ID] {
		c.mu.Unlock()
		return
	}
	c.asked[it.ID] = true
	c.mu.Unlock()
	answered := func(result json.RawMessage, rerr *jsonrpc.Error) {
		c.mu.Lock()
		delete(c.asked, it.ID)
		c.mu.Unlock()
		if rerr != nil {
			return
		}
		if err := c.resolve(threadID, it, result); err != nil {
			c.rpc.Notify(notifyError, ErrorNotification{ThreadID: threadID, TurnID: it.TurnID, ItemID: it.ID, Message: serverError(err).Message})
			if !errors.Is(err, db.ErrNoSuchRequest) {
				c.request(threadID, it)
			}
		}
	}
	if !it.WaitsForApproval() {
		req, err := userinput.Decode(it.Await.Request)
		if err != nil {
			return
		}
		c.rpc.Request(requestUserInput, UserInputRequestParams{ThreadID: threadID, TurnID: it.TurnID, ItemID: it.ID, Questions: nonNil(req.Questions)}, answered)
		return
	}
	args := json.RawMessage(it.Arguments)
	if strings.TrimSpace(it.Arguments) == "" {
		args = json.RawMessage("{}")
	}
	c.rpc.Request(requestApproval, ApprovalRequestParams{
		ThreadID: threadID, TurnID: it.TurnID, ItemID: it.ID, ApprovalID: it.Approval.ID,
		Server: it.Server, Tool: it.Tool, Arguments: args, PreparedExecution: it.PreparedExecution,
		Reason: it.Approval.Reason, Content: it.Approval.Content, SuggestedRules: it.Approval.Suggested, AvailableDecisions: it.Approval.AvailableDecisions(),
	}, answered)
}

func (c *conn) resolve(threadID string, it db.ItemRow, result json.RawMessage) error {
	if !it.WaitsForApproval() {
		var r UserInputResponse
		if err := jsonrpc.DecodeParams(result, &r); err != nil {
			return err
		}
		if r.Skipped {
			return c.s.service.SkipUserInput(c.user, threadID, it.ID)
		}
		return c.s.service.AnswerUserInput(c.user, threadID, it.ID, r.Answers)
	}
	var r ApprovalResponse
	if err := jsonrpc.DecodeParams(result, &r); err != nil {
		return err
	}
	return c.s.service.ResolveApproval(c.user, threadID, it.Approval.ID, r.Decision, "gateway")
}
