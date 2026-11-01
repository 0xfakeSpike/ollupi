package repo

import (
	"slices"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
)

// Completed user and agent messages are found by any substring, Chinese ones of two characters too;
// a message still being written, and anything that is not a message, is not; % and _ in a term are literal,
// and a deleted conversation leaves nothing behind in the index.
func TestSearchFindsMessagesBySubstring(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	if err := db.CreateThread(ThreadRow{ID: "t", Title: "调研", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := db.createTurn(TurnRow{ID: "turn", ThreadID: "t", Seq: 1, Status: TurnInProgress, Model: llm.Model{Name: "m", Connection: "c"}, StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	save := func(it ItemRow) {
		t.Helper()
		it.TurnID, it.StartedAt = "turn", at
		if _, _, err := db.saveItemOf("t", it); err != nil {
			t.Fatal(err)
		}
	}
	found := func(term string, want ...string) {
		t.Helper()
		f, err := db.Search([]string{term}, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range f.Messages {
			got = append(got, m.ItemID)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("search %q found %v, want %v", term, got, want)
		}
	}
	save(ItemRow{ID: "ask", Type: ItemUserMessage, Status: ItemCompleted, Text: "帮我调查下 DeepSeek 的 super-agent 概念"})
	save(ItemRow{ID: "think", Type: ItemReasoning, Status: ItemCompleted, Text: "等待中的对话"})
	answer := ItemRow{ID: "answer", Type: ItemAgentMessage, Status: ItemInProgress, Text: "池化 runtime 让等待中的对话不占资源"}
	save(answer)
	found("等待中")
	answer.Status, answer.Text = ItemCompleted, answer.Text+"，省下 100% 的内存"
	save(answer)

	found("等待中", "answer")
	found("池化", "answer")
	found("deepseek", "ask")
	found("100%", "answer")
	found("1_0")
	found("super_agent")
	indexed := func(want int64) {
		t.Helper()
		var n int64
		if err := db.gorm.Raw("SELECT count(*) FROM items_search_docsize").Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("the search index holds %d messages, want %d", n, want)
		}
	}
	indexed(2)

	if err := db.DeleteThread("t"); err != nil {
		t.Fatal(err)
	}
	found("池化")
	indexed(0)
}
