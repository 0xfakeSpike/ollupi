package repo

import "time"

// TurnsUnderWay counts the turns this shard has to run: queued, ready, running, or waiting on a subagent or on
// background calls, which run here too. A turn waiting on a person asks nothing of the shard meanwhile.
func (d *DB) TurnsUnderWay() (int64, error) {
	var n int64
	err := d.gorm.Model(&TurnRow{}).Where(openTurn+" AND status IN ?",
		[]TurnStatus{TurnQueued, TurnReady, TurnInProgress, TurnWaitingOnAgent, TurnWaitingOnBackground}).Count(&n).Error
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
