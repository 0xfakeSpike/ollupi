package db

import (
	"math"
)

// conversationOrder is the order of a thread's items: by their turns, and within a turn as they were written. A turn
// submitted behind another has its message written first, yet it follows all of that turn.
const conversationOrder = "turn_seq, seq"

// position is where an item stands in its thread's conversationOrder.
type position struct{ TurnSeq, Seq int }

const (
	compactedItem = "type = 'contextCompaction' AND status = 'completed'"
	// carriedItem marks the items before the latest compaction whose effect outlasts it: user messages, which the
	// compacted history quotes; calls allowed for the conversation; and loaded tools and skills, which stay active.
	carriedItem = "type = 'userMessage' OR (type = 'toolCall' AND (approval ->> '$.decision' = 'acceptForSession' OR (status = 'completed' AND (tool = 'load_tools' OR result -> '$.artifacts' IS NOT NULL))))"
)

// latestCompaction is the position of the thread's latest completed compaction up to the turn numbered upTo, or the
// zero position, before every item, when there is none.
func (d *DB) latestCompaction(threadID string, upTo int) (position, error) {
	var at []position
	err := d.gorm.Table("items INDEXED BY items_compactions").Where("thread_id = ? AND turn_seq <= ? AND "+compactedItem, threadID, upTo).
		Order("turn_seq DESC, seq DESC").Limit(1).Select("turn_seq, seq").Scan(&at).Error
	if err != nil || len(at) == 0 {
		return position{}, err
	}
	return at[0], nil
}

// RunItems reads, in order, the items a run of the turn numbered upTo works from: of that turn and those before it,
// every item from the latest completed compaction on, and before it only the carried ones, without their native
// records. The turns after it, which wait for it, are not its conversation yet. A run that needs more of the earlier
// items must widen carriedItem and its index. INDEXED BY makes a condition that drifted from its index an error.
func (d *DB) RunItems(threadID string, upTo int) ([]ItemRow, error) {
	var rows []ItemRow
	err := d.Tx(func(tx *DB) error {
		from, err := tx.latestCompaction(threadID, upTo)
		if err != nil {
			return err
		}
		if err := tx.gorm.Table("items INDEXED BY items_carried").Omit("native").Where("thread_id = ? AND (turn_seq, seq) < (?, ?) AND ("+carriedItem+")", threadID, from.TurnSeq, from.Seq).Order(conversationOrder).Find(&rows).Error; err != nil {
			return err
		}
		var recent []ItemRow
		if err := tx.gorm.Table("items INDEXED BY items_thread_order").Where("thread_id = ? AND (turn_seq, seq) >= (?, ?) AND turn_seq <= ?", threadID, from.TurnSeq, from.Seq, upTo).Order(conversationOrder).Find(&recent).Error; err != nil {
			return err
		}
		rows = append(rows, recent...)
		return nil
	})
	return rows, err
}

func (d *DB) HasImages(threadID string) (bool, error) {
	var n int64
	err := d.Tx(func(tx *DB) error {
		from, err := tx.latestCompaction(threadID, math.MaxInt32)
		if err != nil {
			return err
		}
		return tx.gorm.Model(&ItemRow{}).Where(`thread_id = ? AND type = 'userMessage' AND (turn_seq, seq) > (?, ?)
			AND EXISTS (SELECT 1 FROM json_each(items.attachments) WHERE json_extract(json_each.value, '$.kind') = 'image')`, threadID, from.TurnSeq, from.Seq).Count(&n).Error
	})
	return n > 0, err
}
