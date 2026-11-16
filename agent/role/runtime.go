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

// A Runtime runs a turn as the role it names: one of the roles here, for the deployment's agent, on what its user runs
// on. It holds the turn as Load read it, its conversation and the permissions made there, and records each item the
// run makes, so that the turn's items are its state: each step of Run is read from them.
type Runtime struct {
	LLM         llm.Client
	DB          *db.DB
	Attachments blobs.Store
	Registry    *tools.Registry
	// Events tells those who watch the thread of the items the run records and the text the model streams.
	Events event.Emitter

	role Role
	// agent and prompt are the deployment's agent, by its name and the operator's prompt for it; skillsDir is where
	// each role's skills are installed.
	agent, prompt, skillsDir string
	// permissions are what the deployment permits, on which the run's permissions are made.
	permissions permission.Deployment
	// memory keeps the users' memory, which the runs of a role that recalls take in; without it they take in none.
	memory *memory.Manager
	// window is the context window of the turn's model, which the prompt, the tools and the memory are fitted to.
	window *contextmanager.Manager

	turn      db.TurnRow
	thread    db.ThreadRow
	workspace db.WorkspaceRow
	// history are the conversation's items before the turn's, items the turn's own.
	history, items []db.ItemRow

	checker *permission.Checker
	// redactor keeps the user's secrets, with their key, from the model when masking, as the user's settings say.
	redactor *redact.Redactor
	masking  bool
	usage    db.Usage
}

// finalAnswerNotice tells the model, at a stepConclude, that tool use stopped and why, and asks for its final answer.
const finalAnswerNotice = `[runtime] Tool use has stopped: %s.
Do not call tools. Write the final answer to the user now, in their language: report what was accomplished, citing the observations above; state clearly what could not be done and why; and give the most useful next step.`

// Run runs the turn Load read, a step at a time as the turn's items say, until the model answered all of the turn's
// input, which it reports, or the turn waits.
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
