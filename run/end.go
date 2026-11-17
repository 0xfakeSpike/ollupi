package run

import (
	"context"
	"encoding/json"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/google/uuid"
)

const stoppedWithParent = "stopped with the turn that launched it"

func (r *Runner) finish(ctx context.Context, s *step) error {
	var answer string
	for _, it := range s.v.ItemsOf(s.turn.ID) {
		if it.Kind == conversation.ItemAgentMessage {
			answer = it.Text
		}
	}
	return r.st.Update(ctx, func(tx store.Tx) error { return end(tx, s.th.ID, conversation.Completed, "", answer) })
}

func End(tx store.Tx, thread string, status conversation.End, reason string) error {
	return end(tx, thread, status, reason, "")
}

func end(tx store.Tx, thread string, status conversation.End, reason, answer string) error {
	th, err := tx.Thread(thread)
	if err != nil {
		return err
	}
	v, err := tx.View(thread)
	if err != nil {
		return err
	}
	turn, ok := v.Active()
	if !ok {
		return nil
	}
	items := v.ItemsOf(turn.ID)
	for _, it := range items {
		if it.AwaitsReport() {
			if err := end(tx, it.Child, conversation.Interrupted, stoppedWithParent, ""); err != nil {
				return err
			}
		}
	}
	why := reason
	if why == "" {
		why = "the turn ended before the call did"
	}
	var entries []conversation.Entry
	for _, it := range items {
		open := it.Kind == conversation.ItemToolCall && (it.Status == conversation.Pending || it.Status == conversation.InProgress)
		waitsForAnswer := open && it.Input != nil && !it.Input.Answered
		switch {
		case waitsForAnswer:
			entries = append(entries, conversation.Entry{Turn: turn.ID, Item: it.ID, Body: conversation.Answered{Skipped: true}})
		case open:
			entries = append(entries, conversation.Entry{Turn: turn.ID, Item: it.ID, Body: conversation.ToolResult{Declined: true, Error: why}})
		}
	}
	entries = append(entries, conversation.Entry{Turn: turn.ID, Body: conversation.TurnEnded{Status: status, Reason: reason}})
	if _, err := tx.Append(thread, entries...); err != nil {
		return err
	}
	if th.Parent == "" {
		return nil
	}
	return report(tx, th, v, turn, status, reason, answer)
}

func report(tx store.Tx, th store.Thread, v conversation.View, turn conversation.Turn, status conversation.End, reason, answer string) error {
	parent, err := tx.View(th.Parent)
	if err != nil {
		return err
	}
	launch, _ := parent.ItemOf(th.ParentItem)
	body := conversation.ChildReported{Launch: th.ParentItem, Child: th.ID}
	for _, it := range v.ItemsOf(turn.ID) {
		if it.Kind == conversation.ItemUserMessage {
			body.Task = it.Text
			break
		}
	}
	if status == conversation.Completed {
		body.Output, _ = json.Marshal(answer)
	} else {
		body.Error = reason
	}
	_, err = tx.Append(th.Parent, conversation.Entry{Turn: launch.Turn, Item: uuid.NewString(), Body: body})
	return err
}
