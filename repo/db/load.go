package db

import "time"

// TurnsUnderWay counts the turns this shard has to run: ready, those behind another included, running, or waiting on
// a subagent, which runs here too. A turn waiting on a person asks nothing of the shard meanwhile.
func (d *DB) TurnsUnderWay() (int64, error) {
	var n int64
	err := d.gorm.Model(&TurnRow{}).Where(openTurn+" AND status IN ?",
		[]TurnStatus{TurnReady, TurnInProgress, TurnWaitingOnAgent}).Count(&n).Error
	return n, err
}

// ActiveUsers counts the users with a conversation that moved since then. It looks each user up in threads_list,
// rather than reading every thread.
func (d *DB) ActiveUsers(since time.Time) (int64, error) {
	var n int64
	err := d.gorm.Model(&UserRow{}).Where(`EXISTS (SELECT 1 FROM threads t
		WHERE t.user_id = users.id AND t.parent_thread_id IS NULL AND t.updated_at >= ?)`, since.UTC()).Count(&n).Error
	return n, err
}
