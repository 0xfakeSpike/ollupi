package tools

import (
	"encoding/json"
	"strings"
)

type Result struct {
	Output    any
	Err       error
	Tool      string
	Artifacts []ContextArtifact
	Await     *Await
}

type AwaitBy string

const (
	AwaitUser AwaitBy = "user"
)

var AwaitByValues = []AwaitBy{AwaitUser}

type Await struct {
	By      AwaitBy         `json:"by"`
	Request json.RawMessage `json:"request"`
	Handle  string          `json:"handle,omitempty"`
}

func Awaiting(by AwaitBy, request any) Result {
	raw, _ := json.Marshal(request) // plain data: it cannot fail
	return Result{Await: &Await{By: by, Request: raw}}
}

const (
	ArtifactAgentSkill = "agent_skill"
)

type ContextArtifact struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

func (r Result) For(tool string) Result {
	r.Tool = strings.TrimSpace(tool)
	return r
}
