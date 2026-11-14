package runtime

import (
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
)

// A Host is what a run cannot do on its own data: tell those who watch, and start calls in the background.
type Host interface {
	// Notify tells those who watch the thread of an item the run recorded: a new one, or one that changed.
	Notify(it db.ItemRow, isNew bool)
	// Delta tells those who watch the thread of text the model streams into an item.
	Delta(it db.ItemRow, text string)
	// Background starts a call that asked for it in the background, as caller makes it, and returns at once with what
	// the model is told.
	Background(caller tools.Caller, it db.ItemRow, args map[string]any) tools.Result
}
