package memory

import (
	"encoding/json"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

type Fact struct {
	Source db.MemorySource `json:"source"`
	Data   json.RawMessage `json:"data"`
}

type Batch struct {
	UserID      string          `json:"-"`
	Scope       db.MemoryScope  `json:"scope"`
	WorkspaceID string          `json:"workspaceId,omitempty"`
	ThreadID    string          `json:"threadId"`
	TurnID      string          `json:"turnId"`
	TurnStatus  db.TurnStatus   `json:"turnStatus"`
	TurnError   string          `json:"turnError,omitempty"`
	Facts       []Fact          `json:"facts"`
	Existing    []db.MemoryRow  `json:"existing"`
	Cursor      db.MemoryCursor `json:"-"`
	Entries     int             `json:"-"`
}

type Operation struct {
	Action  string            `json:"action"`
	Scope   string            `json:"scope,omitempty"`
	ID      string            `json:"id,omitempty"`
	Topic   string            `json:"topic,omitempty"`
	Kind    string            `json:"kind,omitempty"`
	Text    string            `json:"text,omitempty"`
	Sources []db.MemorySource `json:"sources"`
}

type Snapshot struct {
	Entries []db.MemoryReference
	Text    string
}
