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
	AwaitUser  AwaitBy = "user"
	AwaitAgent AwaitBy = "agent"
)

var AwaitByValues = []AwaitBy{AwaitUser, AwaitAgent}

type Await struct {
	By       AwaitBy         `json:"by"`
	Request  json.RawMessage `json:"request"`
	ThreadID string          `json:"threadId,omitempty"`
}

func Awaiting(by AwaitBy, request any) (Result, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return Result{}, err
	}
	return Result{Await: &Await{By: by, Request: raw}}, nil
}

const (
	ArtifactAgentSkill = "agent_skill"
)

type ContextArtifact struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Source  string `json:"source,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

func (r Result) For(tool string) Result {
	r.Tool = strings.TrimSpace(tool)
	return r
}
