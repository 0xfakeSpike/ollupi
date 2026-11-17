package role

import (
	"context"
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"github.com/0xfakeSpike/ollupi/repo/blobs"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/uuid"
)

type Runtime struct {
	client      llm.Client
	store       *db.DB
	attachments blobs.Store
	registry    *tools.Registry
	events      event.Emitter

	role                     Role
	agent, prompt, skillsDir string
	permissions              permission.Deployment
	memory                   *memory.Manager
	window                   *contextmanager.Manager

	turn           db.TurnRow
	thread         db.ThreadRow
	workspace      db.WorkspaceRow
	history, items []db.ItemRow

	checker  *permission.Checker
	redactor *redact.Redactor
	masking  bool
	usage    db.Usage
}

const finalAnswerNotice = `[runtime] Tool use has stopped: %s.
Do not call tools. Write the final answer to the user now, in their language: report what was accomplished, citing the observations above; state clearly what could not be done and why; and give the most useful next step.`

func (rt *Runtime) Run(ctx context.Context) (bool, db.Usage, error) {
	for {
		var err error
		switch step, reason := rt.next(); step {
		case stepWait:
			return false, rt.usage, nil
		case stepDone:
			return true, rt.usage, nil
		case stepCalls:
			err = rt.callTools(ctx)
		case stepConclude:
			now := time.Now().UTC()
			_, err = rt.record(db.ItemRow{
				ID: uuid.NewString(), TurnID: rt.turn.ID, Type: db.ItemError, Status: db.ItemCompleted,
				Text: fmt.Sprintf(finalAnswerNotice, reason), StartedAt: now, CompletedAt: &now,
			})
			if err == nil {
				err = rt.respond(ctx, llm.ToolChoiceNone)
			}
			if err != nil {
				err = fmt.Errorf("%w (tool use stopped: %s)", err, reason)
			}
		case stepRespond:
			err = rt.respond(ctx, llm.ToolChoiceAuto)
		}
		if err == nil {
			err = rt.reload()
		}
		if err != nil {
			return false, rt.usage, err
		}
	}
}
