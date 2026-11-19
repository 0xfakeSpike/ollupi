package run

import (
	"context"
	"encoding/json"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/google/uuid"
)

const stoppedWithParent = "stopped with the turn that launched it"

func (r *Runner) finish(ctx context.Context, s *step) error {
	return r.st.Update(ctx, func(tx store.Tx) error { return End(tx, s.th.ID, conversation.Completed, "") })
}

func End(tx store.Tx, thread string, status conversation.End, reason string) error {
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
		if !it.AwaitsReport() {
			continue
		}
		if it.Child != "" {
			err = End(tx, it.Child, conversation.Interrupted, stoppedWithParent)
		} else {
			err = ReportTask(tx, it.Task, tool.Result{Error: stoppedWithParent})
		}
		if err != nil {
			return err
		}
	}
	why := reason
	if why == "" {
		why = "the turn ended before the call did"
	}
	var entries []conversation.Entry
	for _, it := range items {
		switch {
		case it.WaitsForInput():
			entries = append(entries, conversation.Entry{Turn: turn.ID, Item: it.ID, Body: conversation.Answered{Skipped: true}})
		case it.Open():
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
	return report(tx, th, v, turn, status, reason)
}

func report(tx store.Tx, th store.Thread, v conversation.View, turn conversation.Turn, status conversation.End, reason string) error {
	parent, err := tx.View(th.Parent)
	if err != nil {
		return err
	}
	launch, _ := parent.ItemOf(th.ParentItem)
	body := conversation.ChildReported{Launch: th.ParentItem, Child: th.ID}
	var answer string
	for _, it := range v.ItemsOf(turn.ID) {
		switch {
		case it.Kind == conversation.ItemUserMessage && body.Task == "":
			body.Task = it.Text
		case it.Kind == conversation.ItemAgentMessage:
			answer = it.Text
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
