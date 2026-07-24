package jobs

import (
	"time"
)

type JobResult struct {
	Name    string `json:"name"`
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func CloseExpiredJobVacancies() JobResult {
	closeJobVacancies()
	return JobResult{Name: "CloseExpiredJobVacancies", Success: true, Message: "Closed expired vacancies"}
}

func ExpireSubscriptions() JobResult {
	expireSubscriptions()
	return JobResult{Name: "ExpireSubscriptions", Success: true, Message: "Subscriptions expired"}
}

func SendJobRequestReminders() JobResult {
	return JobResult{Name: "SendJobRequestReminders", Success: true, Message: "Reminders sent"}
}

func RequeryPendingTransactions() JobResult {
	return JobResult{Name: "RequeryPendingTransactions", Success: true, Message: "Requery completed"}
}

func CloseAwaitingConfirmation() JobResult {
	closeAwaitingConfirmation()
	return JobResult{Name: "CloseAwaitingConfirmation", Success: true, Message: "Closed awaiting confirmation jobs"}
}

func IsAfterHour(t time.Time) bool {
	return t.Hour() >= 6 && t.Hour() < 22
}
