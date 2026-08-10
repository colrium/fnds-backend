package jobs

import (
	"context"
	"log/slog"
	"time"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
)

type Scheduler struct {
	stop chan struct{}
}

func NewScheduler() *Scheduler {
	return &Scheduler{stop: make(chan struct{})}
}

func (s *Scheduler) Start() {
	go s.run()
}

func (s *Scheduler) Stop() {
	close(s.stop)
}

func (s *Scheduler) run() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	slog.Info("job scheduler started")
	s.executeJobs()

	for {
		select {
		case <-ticker.C:
			s.executeJobs()
		case <-s.stop:
			slog.Info("job scheduler stopped")
			return
		}
	}
}

func (s *Scheduler) executeJobs() {
	slog.Info("executing scheduled jobs")
	closeJobVacancies()
	expireSubscriptions()
	closeAwaitingConfirmation()
	CheckReviewSLA()
	CheckAppointmentReminders()
	CheckNoShows()
}

func closeJobVacancies() {
	_, err := configs.MI.DB.Collection("job_requests").UpdateMany(
		context.TODO(),
		bson.M{"status": "open"},
		bson.M{"$set": bson.M{"status": "closed", "updated_at": time.Now()}},
	)
	if err != nil {
		slog.Error("failed to close job vacancies", "error", err)
	}
}

func expireSubscriptions() {
	_, err := configs.MI.DB.Collection("user_subscriptions").UpdateMany(
		context.TODO(),
		bson.M{"is_active": true, "end_date": bson.M{"$lt": time.Now()}},
		bson.M{"$set": bson.M{
			"is_active":  false,
			"status":     "expired",
			"updated_at": time.Now(),
		}},
	)
	if err != nil {
		slog.Error("failed to expire subscriptions", "error", err)
	}
}

func closeAwaitingConfirmation() {
	result, err := configs.MI.DB.Collection("job_requests").UpdateMany(
		context.TODO(),
		bson.M{
			"status":     "completed",
			"updated_at": bson.M{"$lt": time.Now().Add(-48 * time.Hour)},
		},
		bson.M{"$set": bson.M{"status": "closed", "updated_at": time.Now()}},
	)
	if err != nil {
		slog.Error("failed to close awaiting confirmation jobs", "error", err)
		return
	}
	if result.ModifiedCount > 0 {
		slog.Info("closed awaiting confirmation jobs", "count", result.ModifiedCount)
	}
}
