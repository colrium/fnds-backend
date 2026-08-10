package assessments

import (
	"time"

	"backend/integrations"
)

// zohoMeetingAdapter adapts the concrete Zoho service to MeetingCreator, so this
// package depends on its own narrow interface rather than the integration's
// types. That keeps the booking handler testable with a stub.
type zohoMeetingAdapter struct {
	service *integrations.ZohoMeetingService
}

// NewZohoMeetingAdapter wraps a configured Zoho service for use as a
// MeetingCreator. Returns nil for a nil service so main.go can pass the result
// straight through without a second nil check.
func NewZohoMeetingAdapter(service *integrations.ZohoMeetingService) MeetingCreator {
	if service == nil {
		return nil
	}
	return &zohoMeetingAdapter{service: service}
}

func (a *zohoMeetingAdapter) CreateMeeting(topic string, startTime time.Time, durationMin int) (*MeetingResult, error) {
	result, err := a.service.CreateMeeting(topic, startTime, durationMin)
	if err != nil {
		return nil, err
	}
	return &MeetingResult{
		MeetingKey: result.MeetingKey,
		JoinLink:   result.JoinLink,
		StartLink:  result.StartLink,
	}, nil
}
