package conversation

import (
	"encoding/json"
	"fmt"
)

// Decode reads a body a store kept as JSON beside its kind.
func Decode(kind Kind, raw []byte) (Body, error) {
	var b Body
	var err error
	switch kind {
	case KindTurnQueued:
		b, err = decode[TurnQueued](raw)
	case KindTurnEnded:
		b, err = decode[TurnEnded](raw)
	case KindUserMessage:
		b, err = decode[UserMessage](raw)
	case KindAgentMessage:
		b, err = decode[AgentMessage](raw)
	case KindReasoning:
		b, err = decode[Reasoning](raw)
	case KindModelResponded:
		b, err = decode[ModelResponded](raw)
	case KindModelFailed:
		b, err = decode[ModelFailed](raw)
	case KindToolCall:
		b, err = decode[ToolCall](raw)
	case KindApprovalAsked:
		b, err = decode[ApprovalAsked](raw)
	case KindDecided:
		b, err = decode[Decided](raw)
	case KindToolStarted:
		b, err = decode[ToolStarted](raw)
	case KindInputAsked:
		b, err = decode[InputAsked](raw)
	case KindAnswered:
		b, err = decode[Answered](raw)
	case KindToolResult:
		b, err = decode[ToolResult](raw)
	case KindChildLaunched:
		b, err = decode[ChildLaunched](raw)
	case KindChildReported:
		b, err = decode[ChildReported](raw)
	case KindCompacted:
		b, err = decode[Compacted](raw)
	case KindRecalled:
		b, err = decode[Recalled](raw)
	case KindNotice:
		b, err = decode[Notice](raw)
	default:
		return nil, fmt.Errorf("conversation: no entry of kind %q", kind)
	}
	if err != nil {
		return nil, fmt.Errorf("conversation: a %s entry: %w", kind, err)
	}
	return b, nil
}

func decode[T Body](raw []byte) (Body, error) {
	var b T
	err := json.Unmarshal(raw, &b)
	return b, err
}
