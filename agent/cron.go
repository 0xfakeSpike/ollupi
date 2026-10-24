package agent

import (
	"log"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/google/uuid"
)

func (s *orchestrator) sendDue(threadID string) {
	if s.ctx.Err() != nil {
		return
	}
	now := time.Now().UTC()
	due, err := s.a.DB.DueCronJobs(now)
	if err != nil {
		log.Printf("agent: scheduled tasks: %v", err)
		return
	}
	for _, job := range due {
		if threadID != "" && job.ThreadID != threadID {
			continue
		}
		view, err := s.a.ReadThread(job.ThreadID)
		if repo.IsNotFound(err) {
			continue
		}
		if err != nil {
			log.Printf("agent: scheduled task %s: read thread %s: %v", job.ID, job.ThreadID, err)
			continue
		}
		if _, active := view.ActiveTurn(); active {
			continue
		}
		if err := s.sendJob(view, job); err != nil {
			log.Printf("agent: scheduled task %s could not start in thread %s: %v", job.ID, job.ThreadID, err)
		}
		if err := s.a.cron.Sent(job, now); err != nil {
			log.Printf("agent: scheduled task %s: record its run: %v", job.ID, err)
		}
	}
}

func (s *orchestrator) sendJob(view repo.ThreadView, job repo.CronJobRow) error {
	text, err := checkMessage(job.Prompt)
	if err != nil {
		return err
	}
	model, err := s.a.conversationModel(view, "")
	if err != nil {
		return err
	}
	message := repo.NewUserMessage(uuid.NewString(), "", text, time.Now().UTC())
	message.CronJobID = job.ID
	_, err = s.beginTurn(view, message, model)
	return err
}
