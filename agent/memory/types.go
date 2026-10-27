package memory

import (
	"encoding/json"

	"github.com/0xfakeSpike/ollupi/repo"
)

type Fact struct {
	Source repo.MemorySource `json:"source"`
	Data   json.RawMessage   `json:"data"`
}

type Batch struct {
	Scope       repo.MemoryScope  `json:"scope"`
	WorkspaceID string            `json:"workspaceId,omitempty"`
	ThreadID    string            `json:"threadId"`
	TurnID      string            `json:"turnId"`
	TurnStatus  repo.TurnStatus   `json:"turnStatus"`
	TurnError   string            `json:"turnError,omitempty"`
	Facts       []Fact            `json:"facts"`
	Existing    []repo.MemoryRow  `json:"existing"`
	Cursor      repo.MemoryCursor `json:"-"`
	Entries     int               `json:"-"`
}

type Operation struct {
	Action  string              `json:"action"`
	Scope   string              `json:"scope,omitempty"`
	ID      string              `json:"id,omitempty"`
	Topic   string              `json:"topic,omitempty"`
	Kind    string              `json:"kind,omitempty"`
	Text    string              `json:"text,omitempty"`
	Sources []repo.MemorySource `json:"sources"`
}

type Context struct {
	Entries []repo.MemoryReference
	Text    string
}
