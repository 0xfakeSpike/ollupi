package runtime

import (
	"context"

	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/skills"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

// A Host is what a run asks of the agent that runs it, for the turn it runs.
type Host interface {
	// Record stores an item of the turn and tells those who watch the thread.
	Record(repo.ItemRow) (repo.ItemRow, error)
	// Delta tells those who watch the thread of text the model streams into an item.
	Delta(it repo.ItemRow, text string)
	// Reload reads the turn's items again, with the permissions of its thread as they are now.
	Reload() ([]repo.ItemRow, *permission.Checker, error)
	// Settings are the settings of the turn's user.
	Settings() (repo.SettingsRow, error)
	// Skills are the skills the system prompt may list.
	Skills() []skills.Skill
	// Memory is the snapshot of the user's memory that a run starts with, within budget tokens; nil when the agent
	// keeps no memory.
	Memory(budget int) (*memory.Snapshot, error)
	// Attachment reads a file attached to a message of the thread.
	Attachment(ctx context.Context, path string) ([]byte, error)
	// Background starts a call that asked for it in the background and returns at once with what the model is told.
	Background(it repo.ItemRow, args map[string]any) tools.Result
}
