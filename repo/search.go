package repo

import (
	"strings"
	"time"
	"unicode"

	"gorm.io/gorm"
)

const snippetRunes = 40

type MessageHit struct {
	ThreadID    string    `json:"threadId"`
	ThreadTitle string    `json:"threadTitle"`
	ItemID      string    `json:"itemId"`
	TurnID      string    `json:"turnId"`
	Seq         int       `json:"seq"`
	Type        ItemType  `json:"type"`
	Snippet     string    `json:"snippet"`
	At          time.Time `json:"at"`
}

type Found struct {
	Threads  []ThreadRow   `json:"threads"`
	Messages []MessageHit  `json:"messages"`
	Tasks    []CronJobView `json:"tasks"`
}

// Search finds the conversations, messages and scheduled tasks whose text holds every term, newest first.
func (d *DB) Search(terms []string, workspaceID string, limit int) (Found, error) {
	f := Found{Threads: []ThreadRow{}, Messages: []MessageHit{}, Tasks: []CronJobView{}}
	inWorkspace := func(q *gorm.DB, column string) *gorm.DB {
		if workspaceID == "" {
			return q
		}
		return q.Where("COALESCE("+column+", '') = ?", workspaceID)
	}
	q := d.gorm.Where("parent_thread_id IS NULL")
	for _, t := range terms {
		q = q.Where(`title LIKE ? ESCAPE '\'`, likePattern(t))
	}
	if err := inWorkspace(q, "workspace_id").Order("updated_at DESC, id").Limit(limit).Find(&f.Threads).Error; err != nil {
		return Found{}, err
	}

	var rows []struct {
		ItemRow
		ThreadTitle string
	}
	q = d.gorm.Table("items AS i").Select("i.id, i.thread_id, i.turn_id, i.seq, i.type, i.text, i.started_at, t.title AS thread_title").
		Joins("JOIN threads t ON t.id = i.thread_id").
		Where("t.parent_thread_id IS NULL AND i.type IN ? AND i.status = ?", []ItemType{ItemUserMessage, ItemAgentMessage}, ItemCompleted)
	for _, t := range terms {
		q = q.Where(`i.text LIKE ? ESCAPE '\'`, likePattern(t))
	}
	if err := inWorkspace(q, "t.workspace_id").Order("i.started_at DESC, i.id").Limit(limit).Scan(&rows).Error; err != nil {
		return Found{}, err
	}
	for _, r := range rows {
		f.Messages = append(f.Messages, MessageHit{ThreadID: r.ThreadID, ThreadTitle: r.ThreadTitle, ItemID: r.ID, TurnID: r.TurnID,
			Seq: r.Seq, Type: r.Type, Snippet: snippet(r.Text, terms[0]), At: r.StartedAt})
	}

	q = d.cronJobViews()
	for _, t := range terms {
		q = q.Where(`(j.name || ' ' || j.prompt) LIKE ? ESCAPE '\'`, likePattern(t))
	}
	if err := inWorkspace(q, "t.workspace_id").Order("j.created_at DESC, j.id").Limit(limit).Scan(&f.Tasks).Error; err != nil {
		return Found{}, err
	}
	return f, nil
}

func likePattern(term string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(term) + "%"
}

func snippet(text, term string) string {
	runes := []rune(text)
	lower := make([]rune, len(runes))
	for i, r := range runes {
		lower[i] = unicode.ToLower(r)
	}
	want := []rune(strings.ToLower(term))
	at := 0
	for i := 0; i+len(want) <= len(lower); i++ {
		if string(lower[i:i+len(want)]) == string(want) {
			at = i
			break
		}
	}
	from, to := max(0, at-snippetRunes), min(len(runes), at+len(want)+snippetRunes)
	s := strings.Join(strings.Fields(string(runes[from:to])), " ")
	if from > 0 {
		s = "…" + s
	}
	if to < len(runes) {
		s += "…"
	}
	return s
}
