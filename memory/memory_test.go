package memory

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
)

var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func kept(id string, place Place, topic string, kind Kind, text string) Memory {
	return Memory{ID: id, User: "u", Place: place, Topic: topic, Kind: kind, Text: text, Status: Active,
		Sources: []Source{{Thread: "old", Item: id}}, Created: base, Updated: base}
}

func same(s string) string { return s }

func TestRecallShowsGlobalAndLocalNotesThatFit(t *testing.T) {
	w := Local("w")
	memories := []Memory{
		kept("g", GlobalPlace, "language", Preference, "Answers in Chinese."),
		kept("l", w, "build", Fact, "Builds with make."),
		{ID: "r", Place: Place{Scope: Workspace, Workspace: "w"}, Topic: "old", Text: "Retired.", Status: Retired},
	}
	r := Recall(memories, w, 2000, "how does this build?")
	if !strings.Contains(r.Text, "Global memories") || !strings.Contains(r.Text, "this conversation's workspace") || len(r.Memories) != 2 {
		t.Fatalf("recalled %d: %s", len(r.Memories), r.Text)
	}
	if strings.Contains(r.Text, "Retired.") {
		t.Error("a retired note is recalled")
	}
	for i := range 40 {
		memories = append(memories, kept(fmt.Sprint("x", i), w, fmt.Sprint("topic ", i), Fact, strings.Repeat("a long note ", 20)))
	}
	if r := Recall(memories, w, 1500, "long note"); !strings.Contains(r.Text, "find them with "+SearchTool) {
		t.Errorf("the notes that do not fit are not counted: %s", r.Text)
	}
	if r := Recall(nil, w, 2000, ""); r.Text != "" || r.Memories != nil {
		t.Errorf("nothing to recall: %+v", r)
	}
}

func TestRecallPrefersTheCurrentRequestAndKeepsStablePreferences(t *testing.T) {
	w := Local("w")
	memories := []Memory{
		kept("x", GlobalPlace, "editor", Fact, "Deploys with Helm."),
		kept("y", GlobalPlace, "language", Preference, "Answers in Chinese."),
		kept("z", w, "testing", Procedure, "Runs the Go race detector."),
	}
	r := Recall(memories, w, 2000, "run the go tests")
	if len(r.Memories) != 2 || r.Memories[0].ID != "y" || r.Memories[1].ID != "z" {
		t.Fatalf("recalled %+v", r.Memories)
	}
	if r := Recall(memories, w, 2000, "is this in the helmet?"); len(r.Memories) != 1 || r.Memories[0].ID != "y" {
		t.Fatalf("common words and partial words recalled %+v", r.Memories)
	}
}

func TestSearchFindsActiveNotesByWordsAndHan(t *testing.T) {
	memories := []Memory{
		kept("a", GlobalPlace, "editor", Fact, "Uses vim for editing."),
		kept("b", GlobalPlace, "部署", Preference, "部署前先跑测试。"),
		{ID: "c", Topic: "editor", Text: "Uses emacs.", Status: Forgotten},
	}
	if got := Search(memories, "vim editor", 5); len(got) != 1 || got[0].ID != "a" {
		t.Errorf("by words: %+v", got)
	}
	if got := Search(memories, "怎么部署", 5); len(got) != 1 || got[0].ID != "b" {
		t.Errorf("by Han: %+v", got)
	}
}

type log struct {
	t *testing.T
	v conversation.View
}

func (l *log) add(turn, item string, b conversation.Body) {
	l.t.Helper()
	e := conversation.Entry{Seq: l.v.Head + 1, Turn: turn, Item: item, At: base.Add(time.Duration(l.v.Head) * time.Second), Body: b}
	if err := conversation.Check(l.v, e); err != nil {
		l.t.Fatal(err)
	}
	l.v.Apply(e)
}

func endedTurn(t *testing.T, messages int, size int) conversation.View {
	l := &log{t: t}
	l.add("t", "", conversation.TurnQueued{})
	for i := range messages {
		l.add("t", fmt.Sprint("m", i), conversation.UserMessage{Text: strings.Repeat("x", size)})
	}
	l.add("t", "", conversation.ModelResponded{Response: "r"})
	l.add("t", "c", conversation.ToolCall{Tool: "run_command", Arguments: `{"command":"make"}`, Response: "r"})
	l.add("t", "c", conversation.ToolStarted{})
	l.add("t", "c", conversation.ToolResult{Output: json.RawMessage(`"ok"`)})
	l.add("t", "a", conversation.AgentMessage{Text: "built", Response: "r"})
	l.add("t", "", conversation.TurnEnded{Status: conversation.Completed})
	return l.v
}

func TestATurnsEvidenceLeavesOutWhatAForgottenNoteWasLearnedFrom(t *testing.T) {
	v := endedTurn(t, 2, 10)
	forgotten := Memory{ID: "f", Place: Place{Scope: General}, Topic: "x", Status: Forgotten, Sources: []Source{{Thread: "th", Item: "m1"}}}
	b := Gather("u", "th", "", v, "t", []Memory{forgotten})
	var items []string
	for _, e := range b.Evidence {
		items = append(items, e.Source.Item)
	}
	if strings.Join(items, ",") != "m0,c,a" || b.Scope != General || b.Status != conversation.Completed {
		t.Fatalf("evidence %v of %+v", items, b)
	}
	var call map[string]any
	if err := json.Unmarshal(b.Evidence[1].Data, &call); err != nil || call["tool"] != "run_command" || call["result"] != `"ok"` {
		t.Errorf("the call's evidence: %v %v", call, err)
	}
}

func TestEvidenceIsSlicedToFitAndNotesAreShownBesideIt(t *testing.T) {
	b := Gather("u", "th", "w", endedTurn(t, 6, 1500), "t", nil)
	parts := Split(b, 2500)
	if len(parts) < 2 {
		t.Fatalf("%d slices", len(parts))
	}
	n := 0
	for _, p := range parts {
		n += len(p.Evidence)
		if _, err := Prompt(p, 2500); err != nil {
			t.Error(err)
		}
	}
	if n != len(b.Evidence) {
		t.Errorf("%d pieces sliced of %d", n, len(b.Evidence))
	}
	small := Gather("u", "th", "w", endedTurn(t, 1, 10), "t", []Memory{kept("k", Local("w"), "build", Fact, "Builds with make.")})
	payload, err := Prompt(small, 8000)
	if err != nil || !strings.Contains(payload, `"id":"k"`) {
		t.Errorf("the note is not shown: %v %s", err, payload)
	}
}

func TestParseKeepsScopesAndRefusesWhatItCannotTrust(t *testing.T) {
	global := kept("g", GlobalPlace, "language", Preference, "Answers in English.")
	edited := kept("e", Local("w"), "style", Preference, "Short answers.")
	edited.UserEdited = true
	b := Batch{Place: Place{Scope: Workspace, Workspace: "w"}, Thread: "th", Turn: "t",
		Evidence: []Evidence{{Source: Source{Thread: "th", Item: "i1"}, Data: []byte(`{}`)}}, Existing: []Memory{global, edited}}
	op := func(fields, sources string) string {
		return `{"operations":[{` + fields + `,"sources":[` + sources + `]}]}`
	}
	fresh := `{"thread":"th","item":"i1"}`
	for _, c := range []struct {
		content, err, scope string
	}{
		{op(`"action":"add","scope":"global","topic":"Editor","kind":"preference","text":"Uses vim."`, fresh), "", "global"},
		{op(`"action":"add","scope":"workspace","topic":"Build","kind":"fact","text":"Builds with make."`, fresh), "", ""},
		{op(`"action":"add","scope":"global","topic":"Build","kind":"decision","text":"Builds with make."`, fresh), "", ""},
		{op(`"action":"update","id":"g","topic":"language","text":"Answers in Chinese.","kind":"preference"`, fresh), "", "global"},
		{op(`"action":"update","id":"g","topic":"language","text":"Ships on Fridays.","kind":"procedure"`, fresh), "only preferences and facts", ""},
		{op(`"action":"update","id":"g","topic":"tongue","text":"Answers in Chinese.","kind":"preference"`, fresh), "", "global"},
		{op(`"action":"add","topic":"Build","kind":"fact","text":"Builds."`, `{"thread":"th","item":"nope"}`), "unknown evidence", ""},
		{op(`"action":"update","id":"g","topic":"language","text":"Answers in Chinese.","kind":"preference"`, `{"thread":"old","item":"g"}`), "no current evidence", ""},
		{op(`"action":"update","id":"missing","text":"x","kind":"fact"`, fresh), "unavailable", ""},
	} {
		ops, err := Parse(c.content, b, same)
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: got %v, want an error about %q", c.content, err, c.err)
		case c.err == "" && err != nil:
			t.Errorf("%s: %v", c.content, err)
		case c.err == "" && ops[0].Scope != c.scope:
			t.Errorf("%s: scope %q, want %q", c.content, ops[0].Scope, c.scope)
		}
	}
	ops, err := Parse(op(`"action":"update","id":"e","topic":"style","text":"Long answers.","kind":"preference"`, fresh), b, same)
	if err != nil || len(ops) != 0 {
		t.Errorf("an edit of the user's is changed: %+v %v", ops, err)
	}
	ops, err = Parse(op(`"action":"update","id":"g","topic":"tongue","text":"Answers in Chinese.","kind":"preference"`, fresh), b, same)
	if err != nil || len(ops) != 1 || ops[0].Topic != "language" {
		t.Errorf("a rewritten topic is kept: %+v %v", ops, err)
	}
	ops, err = Parse(op(`"action":"retire","id":"g","topic":"tongue"`, fresh), b, same)
	if err != nil || len(ops) != 1 || ops[0].Topic != "language" {
		t.Errorf("a rewritten topic on a retire is kept: %+v %v", ops, err)
	}
}

func TestApplyAddsToAKeptTopicAndPlacesNotesByScope(t *testing.T) {
	local := kept("l", Local("w"), "build", Fact, "Builds with go.")
	forgotten := Memory{ID: "f", Place: Place{Scope: Workspace, Workspace: "w"}, Topic: "secret", Status: Forgotten, Sources: []Source{{Thread: "th", Item: "bad"}}}
	b := Batch{User: "u", Place: Place{Scope: Workspace, Workspace: "w"}, Existing: []Memory{local, forgotten}}
	src := []Source{{Thread: "th", Item: "i1"}}
	ops := []Operation{
		{Action: Add, Topic: "build", Kind: Fact, Text: "Builds with make.", Sources: src},
		{Action: Add, Scope: "global", Topic: "editor", Kind: Preference, Text: "Uses vim.", Sources: src},
		{Action: Add, Topic: "secret", Kind: Fact, Text: "Back again.", Sources: src},
		{Action: Add, Topic: "other", Kind: Fact, Text: "From forgotten evidence.", Sources: []Source{{Thread: "th", Item: "bad"}}},
	}
	n := 0
	changed := Apply(b, ops, base.Add(time.Hour), func() string { n++; return fmt.Sprint("new", n) })
	if len(changed) != 2 {
		t.Fatalf("changed %+v", changed)
	}
	if changed[0].ID != "l" || changed[0].Text != "Builds with make." || !changed[0].Updated.Equal(base.Add(time.Hour)) {
		t.Errorf("the kept topic: %+v", changed[0])
	}
	if g := changed[1]; g.Scope != Global || g.Workspace != "" || g.Origin != "w" || g.User != "u" {
		t.Errorf("the global note: %+v", g)
	}
}

func TestTheUserMovesEditsAndForgetsNotes(t *testing.T) {
	m := kept("l", Local("w"), "editor", Preference, "Uses vim.")
	g, err := Move(m, nil, base)
	if err != nil || g.Scope != Global || g.Origin != "w" || g.Workspace != "" {
		t.Fatalf("moved global: %+v %v", g, err)
	}
	back, err := Move(g, nil, base)
	if err != nil || back.Place != Local("w") || back.Origin != "" {
		t.Fatalf("moved back: %+v %v", back, err)
	}
	if _, err := Move(m, []Memory{kept("x", GlobalPlace, "editor", Fact, "")}, base); err == nil {
		t.Error("moved onto a kept topic")
	}
	e, err := Edit(m, "  Uses helix. ", base)
	if err != nil || e.Text != "Uses helix." || !e.UserEdited {
		t.Errorf("edited: %+v %v", e, err)
	}
	f := Forget(m, base)
	if _, err := Edit(f, "again", base); err == nil || f.Text != "" {
		t.Error("a forgotten note is edited")
	}
	if err := (Place{Scope: Workspace}).Check(); err == nil {
		t.Error("a workspace place without a workspace")
	}
}

func TestATurnThatKeepsFailingIsSkipped(t *testing.T) {
	var s State
	turn := Cursor{Seq: 3, Thread: "th", Turn: "t"}
	for range MaxAttempts - 1 {
		s = s.Failed(turn, Failure{Code: ModelRequest, Error: "down"}, base)
		if s.Cursor == turn || s.Due(base) {
			t.Fatalf("given up early: %+v", s)
		}
	}
	s = s.Failed(turn, Failure{Code: ModelRequest, Error: "down"}, base)
	if s.Cursor != turn || s.Skipped != 1 || s.LastSkip == nil || !s.Due(base) {
		t.Fatalf("not skipped: %+v", s)
	}
	s = s.Read(Cursor{Seq: 4}, base)
	if s.Cursor.Seq != 4 || s.LastSuccess == nil || s.Attempts != 0 {
		t.Errorf("after a success: %+v", s)
	}
}
