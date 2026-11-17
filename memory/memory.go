// Package memory is what a user's long-term memory is and the rules it keeps: which notes a conversation recalls,
// what a finished turn offers as evidence, how a model's operations on the notes are checked and applied, and how
// the user corrects them. It reads and writes nothing; the store keeps the notes, and the extract flow feeds them.
package memory

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/conversation"
)

var ErrInvalidChange = errors.New("invalid memory change")

type Scope string

const (
	Global Scope = conversation.GlobalScope
	// General is the scope of the conversations outside any workspace.
	General   Scope = "general"
	Workspace Scope = "workspace"
)

var Scopes = []Scope{Global, General, Workspace}

type Kind string

const (
	Preference Kind = "preference"
	Decision   Kind = "decision"
	Fact       Kind = "fact"
	Procedure  Kind = "procedure"
)

var Kinds = []Kind{Preference, Decision, Fact, Procedure}

type Status string

const (
	Active  Status = "active"
	Retired Status = "retired"
	// Forgotten keeps a note's topic and sources, without its text, so that nothing learns it again.
	Forgotten Status = "forgotten"
)

var Statuses = []Status{Active, Retired, Forgotten}

// Place is where notes are kept: the global scope, the general one, or one workspace's.
type Place struct {
	Scope     Scope
	Workspace string
}

// Local is the place of a conversation in workspace, or outside any when it is "".
func Local(workspace string) Place {
	if workspace == "" {
		return Place{Scope: General}
	}
	return Place{Scope: Workspace, Workspace: workspace}
}

var GlobalPlace = Place{Scope: Global}

func (p Place) Check() error {
	if !slices.Contains(Scopes, p.Scope) {
		return fmt.Errorf("%w: the scope must be one of %v", ErrInvalidChange, Scopes)
	}
	if (p.Scope == Workspace) != (p.Workspace != "") {
		return fmt.Errorf("%w: give a workspaceId with the workspace scope, and only with it", ErrInvalidChange)
	}
	return nil
}

// Source is a piece of evidence a note was learned from: an item of a conversation.
type Source struct {
	Thread string `json:"thread"`
	Item   string `json:"item"`
}

// Memory is one note. Origin is the workspace a global note was learned or moved from, where moving it back puts it.
type Memory struct {
	ID         string    `json:"id"`
	User       string    `json:"-"`
	Scope      Scope     `json:"scope"`
	Workspace  string    `json:"workspaceId,omitempty"`
	Origin     string    `json:"originWorkspaceId,omitempty"`
	Topic      string    `json:"topic"`
	Kind       Kind      `json:"kind"`
	Text       string    `json:"text"`
	Status     Status    `json:"status"`
	UserEdited bool      `json:"userEdited,omitempty"`
	Sources    []Source  `json:"sources"`
	Created    time.Time `json:"createdAt"`
	Updated    time.Time `json:"updatedAt"`
}

func (m Memory) Place() Place { return Place{Scope: m.Scope, Workspace: m.Workspace} }

func (m Memory) Ref() conversation.MemoryRef {
	return conversation.MemoryRef{ID: m.ID, Topic: m.Topic, Text: m.Text, Scope: string(m.Scope), WorkspaceID: m.Workspace}
}

const maxTextRunes = 4000

// Edit is the user's correction: from then on extraction leaves the note as the user wrote it.
func Edit(m Memory, text string, now time.Time) (Memory, error) {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > maxTextRunes {
		return Memory{}, fmt.Errorf("%w: memory text must contain 1 to %d characters", ErrInvalidChange, maxTextRunes)
	}
	if m.Status == Forgotten {
		return Memory{}, fmt.Errorf("%w: a forgotten memory cannot be edited", ErrInvalidChange)
	}
	m.Text, m.UserEdited, m.Status, m.Updated = text, true, Active, now
	return m, nil
}

// Move takes a local note to the global scope, or a global one back to where it came from; there are the notes kept
// where it goes.
func Move(m Memory, there []Memory, now time.Time) (Memory, error) {
	if m.Status == Forgotten {
		return Memory{}, fmt.Errorf("%w: a forgotten memory cannot be moved", ErrInvalidChange)
	}
	taken := slices.ContainsFunc(there, func(o Memory) bool { return o.Topic == m.Topic })
	if taken {
		return Memory{}, fmt.Errorf("%w: a memory with the topic %q is already there; forget or edit that one instead", ErrInvalidChange, m.Topic)
	}
	to, origin := Destination(m), ""
	if to.Scope == Global {
		origin = m.Workspace
	}
	m.Scope, m.Workspace, m.Origin, m.Updated = to.Scope, to.Workspace, origin, now
	return m, nil
}

// Destination is where Move takes m.
func Destination(m Memory) Place {
	if m.Scope == Global {
		return Local(m.Origin)
	}
	return GlobalPlace
}

func Forget(m Memory, now time.Time) Memory {
	m.Status, m.Text, m.Updated = Forgotten, "", now
	return m
}
