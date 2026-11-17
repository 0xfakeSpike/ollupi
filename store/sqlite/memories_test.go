package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/store"
)

func TestNotesAreKeptByPlaceAndATopicOncePerPlace(t *testing.T) {
	s := open(t, t.TempDir())
	start(t, s, "u", "th")
	now := time.Now().UTC()
	at := func(id string, p memory.Place, topic string) memory.Memory {
		return memory.Memory{ID: id, User: "u", Scope: p.Scope, Workspace: p.Workspace, Topic: topic, Kind: memory.Fact, Text: id,
			Status: memory.Active, Sources: []memory.Source{{Thread: "th", Item: "m1"}}, Created: now, Updated: now}
	}
	var heard []store.Event
	s.Feed().Subscribe("u", func(e store.Event) { heard = append(heard, e) })
	update(t, s, func(tx store.Tx) error {
		for _, m := range []memory.Memory{at("g", memory.GlobalPlace, "editor"), at("w", memory.Local("w"), "editor"), at("x", memory.Local("x"), "build")} {
			if err := tx.PutMemory(m); err != nil {
				return err
			}
		}
		return nil
	})
	if len(heard) != 3 || heard[0].Owned == nil || heard[0].Owned.Memory == nil || heard[0].Owned.Memory.ID != "g" {
		t.Fatalf("heard %+v", heard)
	}
	got := read(t, s, func(r store.Reader) ([]memory.Memory, error) {
		return r.Memories("u", memory.GlobalPlace, memory.Local("w"))
	})
	if len(got) != 2 || got[0].Sources[0] != (memory.Source{Thread: "th", Item: "m1"}) || !got[0].Updated.Equal(now) {
		t.Fatalf("at two places: %+v", got)
	}
	if all := read(t, s, func(r store.Reader) ([]memory.Memory, error) { return r.Memories("u") }); len(all) != 3 {
		t.Errorf("every place: %d", len(all))
	}
	err := s.Update(context.Background(), func(tx store.Tx) error { return tx.PutMemory(at("dup", memory.Local("w"), "editor")) })
	if err == nil {
		t.Error("two notes at one place share a topic")
	}
	if _, err := s.readMemory("v", "g"); err == nil {
		t.Error("a note is read by another user")
	}
}

func (s *Store) readMemory(owner, id string) (memory.Memory, error) {
	var m memory.Memory
	err := s.Read(context.Background(), func(r store.Reader) (err error) { m, err = r.Memory(owner, id); return err })
	return m, err
}

func TestEndedTurnsAreReadInTheOrderTheyEnded(t *testing.T) {
	s := open(t, t.TempDir())
	start(t, s, "u", "b")
	start(t, s, "u", "a")
	end := func(thread string) {
		update(t, s, func(tx store.Tx) error {
			_, err := tx.Append(thread, conversation.Entry{Turn: "t1", Body: conversation.TurnEnded{Status: conversation.Completed}})
			return err
		})
	}
	end("b")
	update(t, s, func(tx store.Tx) error {
		if _, err := tx.Append("a", conversation.Entry{Turn: "t1", Body: conversation.TurnEnded{Status: conversation.Completed}}); err != nil {
			return err
		}
		return tx.SetMemoryState("u", memory.State{Cursor: memory.Cursor{Seq: 1}, Skipped: 2})
	})
	ended := read(t, s, func(r store.Reader) ([]memory.Cursor, error) { return r.Ended("u", memory.Cursor{}, 10) })
	if len(ended) != 2 || ended[0].Thread != "b" || ended[1].Thread != "a" || ended[0].Seq >= ended[1].Seq {
		t.Fatalf("ended %+v", ended)
	}
	if after := read(t, s, func(r store.Reader) ([]memory.Cursor, error) { return r.Ended("u", ended[0], 10) }); len(after) != 1 || after[0] != ended[1] {
		t.Errorf("after the first: %+v", after)
	}
	state := read(t, s, func(r store.Reader) (memory.State, error) { return r.MemoryState("u") })
	if state.Cursor.Seq != 1 || state.Skipped != 2 {
		t.Errorf("state %+v", state)
	}
	if none := read(t, s, func(r store.Reader) (memory.State, error) { return r.MemoryState("v") }); none.Cursor != (memory.Cursor{}) {
		t.Errorf("a user never read: %+v", none)
	}
}
