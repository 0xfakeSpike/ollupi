package runtime

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/uuid"
)

// queueBackground queues a call to run in the background, for the orchestrator to start, and returns at once with
// what the model is told: the call's id, under which its result comes back when it ends.
func (r *run) queueBackground(it repo.ItemRow, args map[string]any) tools.Result {
	raw, err := json.Marshal(args)
	if err != nil {
		return tools.Result{Err: fmt.Errorf("encode the arguments: %w", err)}.For(it.Tool)
	}
	call := repo.BackgroundCallRow{ID: uuid.NewString(), ThreadID: r.t.ID, ItemID: it.ID, Tool: it.Tool, Arguments: string(raw), StartedAt: time.Now()}
	if err := r.cfg.DB.QueueBackgroundCall(r.t.UserID, call, r.cfg.BackgroundCalls); err != nil {
		return tools.Result{Err: err}.For(it.Tool)
	}
	r.cfg.Queued()
	return tools.Result{Output: map[string]any{
		"background": call.ID, "status": string(repo.BackgroundQueued),
		"note": "It starts in the background at once; its result comes back to this conversation when it ends.",
	}}
}
