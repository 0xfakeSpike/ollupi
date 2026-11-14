package runtime

import (
	"testing"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

func TestOnlyABackgroundToolLosesRunInBackground(t *testing.T) {
	of := offer{offered: []tools.ToolDescriptor{{Name: "slow", Background: true}, {Name: "quick"}}}
	args, background, err := of.arguments(repo.ItemRow{Tool: "slow", Arguments: `{"cmd":"x","run_in_background":true}`})
	if err != nil || !background || len(args) != 1 || args["cmd"] != "x" {
		t.Errorf("a background call: %v %v %v", args, background, err)
	}
	args, background, err = of.arguments(repo.ItemRow{Tool: "quick", Arguments: `{"run_in_background":true}`})
	if err != nil || background || args[tools.ArgBackground] != true {
		t.Errorf("another tool's own argument of that name: %v %v %v", args, background, err)
	}
	if _, _, err := of.arguments(repo.ItemRow{Tool: "slow", Arguments: `{"run_in_background":"yes"}`}); err == nil {
		t.Error("a run_in_background that is no boolean was taken")
	}
}
