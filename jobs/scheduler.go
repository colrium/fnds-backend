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
	resendInsuranceMail()
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
		bson.M{"$set": bson.M{"is_active": false, "updated_at": time.Now()}},
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

func resendInsuranceMail() {
	cursor, err := configs.MI.DB.Collection("insurances").Find(
		context.TODO(),
		bson.M{"sent": false},
	)
	if err != nil {
		slog.Error("failed to query unsent insurances", "error", err)
		return
	}
	defer cursor.Close(context.TODO())

	type insuranceDoc struct {
		ID string `bson:"_id"`
	}
	var insurances []insuranceDoc
	if err := cursor.All(context.TODO(), &insurances); err != nil {
		slog.Error("failed to decode insurances", "error", err)
		return
	}

	for _, ins := range insurances {
		_, err := configs.MI.DB.Collection("insurances").UpdateOne(
			context.TODO(),
			bson.M{"_id": ins.ID},
			bson.M{"$set": bson.M{"sent": true, "updated_at": time.Now()}},
		)
		if err != nil {
			slog.Error("failed to mark insurance as sent", "id", ins.ID, "error", err)
		}
	}

	if len(insurances) > 0 {
		slog.Info("marked unsent insurances as sent", "count", len(insurances))
	}
}
