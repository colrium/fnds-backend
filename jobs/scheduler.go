package jobs

import (
	"context"
	"log/slog"
	"time"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
)

// JobFunc is a function that can be registered with the scheduler.
type JobFunc func()

// Scheduler runs registered jobs on two tickers: an hourly ticker for
// general maintenance and a 15-minute ticker for faster-cycle work such
// as the Stytch member-delete retry. It also runs daily jobs at a fixed
// time (09:00 EAT).
type Scheduler struct {
	stop         chan struct{}
	hourlyJobs   []JobFunc
	frequentJobs []JobFunc
	dailyJobs    []JobFunc
}

func NewScheduler() *Scheduler {
	s := &Scheduler{stop: make(chan struct{})}
	// Register the original hourly jobs by default.
	s.hourlyJobs = []JobFunc{
		closeJobVacancies,
		expireSubscriptions,
		closeAwaitingConfirmation,
		CheckAppointmentReminders,
		CheckNoShows,
	}
	// Daily jobs run once at 09:00 EAT.
	s.dailyJobs = []JobFunc{
		CheckReviewSLA,
	}
	return s
}

// RegisterHourly adds a job that runs on the 1-hour ticker.
func (s *Scheduler) RegisterHourly(fn JobFunc) {
	s.hourlyJobs = append(s.hourlyJobs, fn)
}

// RegisterFrequent adds a job that runs on the 15-minute ticker.
func (s *Scheduler) RegisterFrequent(fn JobFunc) {
	s.frequentJobs = append(s.frequentJobs, fn)
}

// RegisterDaily adds a job that runs once daily at 09:00 EAT.
func (s *Scheduler) RegisterDaily(fn JobFunc) {
	s.dailyJobs = append(s.dailyJobs, fn)
}

func (s *Scheduler) Start() {
	go s.runHourly()
	go s.runFrequent()
	go s.runDaily()
}

func (s *Scheduler) Stop() {
	close(s.stop)
}

func (s *Scheduler) runHourly() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	slog.Info("job scheduler started", "ticker", "hourly")
	s.executeHourlyJobs()

	for {
		select {
		case <-ticker.C:
			s.executeHourlyJobs()
		case <-s.stop:
			slog.Info("job scheduler stopped", "ticker", "hourly")
			return
		}
	}
}

func (s *Scheduler) runFrequent() {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	slog.Info("job scheduler started", "ticker", "15m")
	s.executeFrequentJobs()

	for {
		select {
		case <-ticker.C:
			s.executeFrequentJobs()
		case <-s.stop:
			slog.Info("job scheduler stopped", "ticker", "15m")
			return
		}
	}
}

// runDaily executes daily jobs once at 09:00 EAT (Africa/Nairobi, UTC+3).
// It sleeps until the next 09:00 EAT, fires, then repeats every 24 hours.
func (s *Scheduler) runDaily() {
	eat, err := time.LoadLocation("Africa/Nairobi")
	if err != nil {
		// Fallback: fixed UTC+3 offset if tz database is unavailable.
		eat = time.FixedZone("EAT", 3*60*60)
	}

	slog.Info("job scheduler started", "ticker", "daily-0900-EAT")

	for {
		now := time.Now().In(eat)
		// Next 09:00 EAT today or tomorrow.
		next := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, eat)
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		delay := next.Sub(now)

		select {
		case <-time.After(delay):
			s.executeDailyJobs()
		case <-s.stop:
			slog.Info("job scheduler stopped", "ticker", "daily-0900-EAT")
			return
		}
	}
}

func (s *Scheduler) executeDailyJobs() {
	if len(s.dailyJobs) == 0 {
		return
	}
	slog.Info("executing daily scheduled jobs", "count", len(s.dailyJobs))
	for _, fn := range s.dailyJobs {
		fn()
	}
}

func (s *Scheduler) executeHourlyJobs() {
	slog.Info("executing hourly scheduled jobs", "count", len(s.hourlyJobs))
	for _, fn := range s.hourlyJobs {
		fn()
	}
}

func (s *Scheduler) executeFrequentJobs() {
	if len(s.frequentJobs) == 0 {
		return
	}
	slog.Info("executing frequent scheduled jobs", "count", len(s.frequentJobs))
	for _, fn := range s.frequentJobs {
		fn()
	}
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
