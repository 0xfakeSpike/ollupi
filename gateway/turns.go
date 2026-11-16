package gateway

import (
	"context"
	"strings"

	"github.com/0xfakeSpike/ollupi/agent/service"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
)

func (c *conn) turnStart(ctx context.Context, p TurnStartParams) (TurnResult, func(), error) {
	in, err := agentInput(p.Input)
	if err != nil {
		return TurnResult{}, nil, err
	}
	in.Connection = p.Connection
	turn, how, err := c.s.service.StartTurn(ctx, c.user, p.ThreadID, in)
	if err != nil {
		return TurnResult{}, nil, err
	}
	return TurnResult{Turn: turnRow(turn), Submission: how}, nil, nil
}

func (c *conn) turnSteer(ctx context.Context, p TurnSteerParams) (TurnSteerResult, func(), error) {
	if p.ExpectedTurnID == "" {
		return TurnSteerResult{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "expectedTurnId is required")
	}
	in, err := agentInput(p.Input)
	if err != nil {
		return TurnSteerResult{}, nil, err
	}
	turn, err := c.s.service.SteerTurn(ctx, c.user, p.ThreadID, p.ExpectedTurnID, in)
	if err != nil {
		return TurnSteerResult{}, nil, err
	}
	return TurnSteerResult{TurnID: turn.ID}, nil, nil
}

func (c *conn) turnInterrupt(ctx context.Context, p TurnInterruptParams) (Empty, func(), error) {
	if strings.TrimSpace(p.TurnID) == "" {
		return Empty{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "turnId is required")
	}
	return Empty{}, nil, c.s.service.InterruptTurn(c.user, p.ThreadID, p.TurnID)
}

func agentInput(input []UserInput) (service.Input, error) {
	var in service.Input
	texts := make([]string, 0, len(input))
	for _, part := range input {
		switch part.Type {
		case "text":
			texts = append(texts, part.Text)
		case "file":
			in.Attachments = append(in.Attachments, service.AttachmentInput{Path: part.Path, Name: part.Name, MimeType: part.MimeType})
		default:
			return service.Input{}, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "input type %q is not supported", part.Type)
		}
	}
	in.Text = strings.Join(texts, "\n")
	return in, nil
}
