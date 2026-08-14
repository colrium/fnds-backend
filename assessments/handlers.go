package assessments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"backend/configs"
	"backend/contextutil"
	"backend/integrations"
	"backend/responses"
	"backend/skills"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
)

var (
	// opsSlackClient is reused across all notifyOps calls to avoid creating
	// a new HTTP client on every notification.
	opsSlackClient *integrations.SlackActivityClient
)

func init() {
	webhookURL := configs.SlackOpsNotifsURL()
	if webhookURL == "" {
		webhookURL = configs.SlackWebhookURL()
	}
	if webhookURL == "" {
		// Announced once here rather than silently: without it, business-critical
		// events (journey starts, profile decisions, bookings, missing meeting
		// links) are dropped with no indication that alerting is off.
		slog.Warn("assessment ops notifications disabled; set SLACK_OPS_NOTIFS_URL or SLACK_WEBHOOK_URL to enable")
		return
	}
	opsSlackClient = integrations.NewSlackActivityClient(webhookURL)
}

// --- Artisan handlers ---

func HandleGetJourney(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}

	journey, err := GetJourneyForUser(c.Request().Context(), userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "no assessment journey found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load journey")
	}
	return responses.SendOK(c, journey, "Journey retrieved")
}

func HandleStartJourney(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}

	journey, err := StartJourney(c.Request().Context(), userID)
	if err != nil {
		if errors.Is(err, ErrProfileIncomplete) {
			return responses.SendError(c, http.StatusBadRequest, err.Error())
		}
		if errors.Is(err, ErrProfileNotApproved) {
			return responses.SendError(c, http.StatusBadRequest, err.Error())
		}
		if errors.Is(err, ErrJourneyExists) {
			return responses.SendError(c, http.StatusConflict, err.Error())
		}
		slog.Error("failed to start journey", "user_id", userID, "error", err)
		return responses.SendError(c, http.StatusInternalServerError, "failed to start journey")
	}
	slog.Info("assessment journey started", "journey_id", journey.ID, "user_id", userID, "skill_id", journey.SkillID)
	notifyOps(c.Request().Context(), "New Assessment Journey", fmt.Sprintf("User %s started assessment journey *%s* for skill %s.", userID, journey.ID, journey.SkillID))
	return responses.SendCreated(c, journey, "Assessment journey started")
}

func HandleStartWrittenAttempt(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}

	attempt, err := StartWrittenAttempt(c.Request().Context(), userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "no journey found")
		}
		if errors.Is(err, ErrStageNotAvailable) || errors.Is(err, ErrCooldownActive) || errors.Is(err, ErrAttemptOpen) {
			return responses.SendError(c, http.StatusBadRequest, err.Error())
		}
		if errors.Is(err, ErrInsufficientBank) {
			return responses.SendError(c, http.StatusServiceUnavailable, err.Error())
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to start attempt")
	}
	return responses.SendCreated(c, attempt, "Written attempt started")
}

func HandleSaveAnswer(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}

	attemptID := c.Param("id")
	if attemptID == "" {
		return responses.SendError(c, http.StatusBadRequest, "attempt id is required")
	}

	var answer AttemptAnswer
	if err := c.Bind(&answer); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	if strings.TrimSpace(answer.QuestionID) == "" {
		return responses.SendError(c, http.StatusBadRequest, "question_id is required")
	}

	if err := RecordAnswer(c.Request().Context(), attemptID, userID, answer); err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "attempt not found")
		}
		if errors.Is(err, ErrUnknownQuestion) {
			return responses.SendError(c, http.StatusBadRequest, err.Error())
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to save answer")
	}
	return responses.SendOK(c, nil, "Answer saved")
}

func HandlePauseAttempt(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	attemptID := c.Param("id")
	if err := SetAttemptPaused(c.Request().Context(), attemptID, userID, true); err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "attempt not found or already paused")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to pause attempt")
	}
	return responses.SendOK(c, nil, "Attempt paused")
}

func HandleResumeAttempt(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	attemptID := c.Param("id")
	if err := SetAttemptPaused(c.Request().Context(), attemptID, userID, false); err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "attempt not found or not paused")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to resume attempt")
	}
	return responses.SendOK(c, nil, "Attempt resumed")
}

func HandleSubmitAttempt(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	attemptID := c.Param("id")
	attempt, err := SubmitAttempt(c.Request().Context(), attemptID, userID, nil)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "attempt not found or already submitted")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to submit attempt")
	}
	return responses.SendOK(c, attempt, "Attempt submitted and scored")
}

func HandleListAvailability(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	stage := Stage(c.QueryParam("stage"))
	centreID := c.QueryParam("centre_id")
	now := time.Now().UTC()
	slots, err := ListOpenSlots(c.Request().Context(), stage, centreID, now)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list availability")
	}
	return responses.SendOK(c, slots, "Available slots retrieved")
}

// appointmentDetail enriches an appointment with the display names an artisan
// needs. The ids alone are useless to a client, and resolving them per-field
// would cost the frontend three more round trips.
type appointmentDetail struct {
	*Appointment
	SkillName     string  `json:"skill_name,omitempty"`
	AssessorName  string  `json:"assessor_name,omitempty"`
	CentreName    string  `json:"centre_name,omitempty"`
	CentreAddress string  `json:"centre_address,omitempty"`
	TaskName      string  `json:"task_name,omitempty"`
	EndsAt        *string `json:"ends_at,omitempty"`
	// CanReschedule mirrors the server's own guard so the client does not have to
	// duplicate the cutoff rule to decide whether to show the button.
	CanReschedule       bool   `json:"can_reschedule"`
	RescheduleBlockedBy string `json:"reschedule_blocked_by,omitempty"`
}

// rescheduleCutoff is resolved once at init rather than per request so a bad
// value cannot change behaviour mid-run. See configs.AssessmentRescheduleCutoff.
var rescheduleCutoff = configs.AssessmentRescheduleCutoff()

// HandleGetMyAppointment returns the artisan's current appointment for a stage,
// enriched with assessor, skill and venue names.
func HandleGetMyAppointment(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	stage := Stage(strings.TrimSpace(c.QueryParam("stage")))
	if stage != StageOral && stage != StagePractical {
		return responses.SendError(c, http.StatusBadRequest, "stage must be oral or practical")
	}
	ctx := c.Request().Context()
	appointment, err := GetActiveAppointmentForUserStage(ctx, userID, stage)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "no scheduled appointment for this stage")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load appointment")
	}

	detail := &appointmentDetail{Appointment: appointment}

	// Every enrichment below is best-effort: a missing skill, deleted assessor or
	// deactivated centre must not stop the artisan seeing their appointment time.
	if journey, err := GetJourneyByID(ctx, appointment.JourneyID); err == nil {
		if skill, err := skills.GetSkillByID(ctx, journey.SkillID); err == nil && skill != nil {
			detail.SkillName = skill.Name
		}
	}
	if appointment.AssessorID != "" {
		if assessor, err := users.GetUserByID(ctx, appointment.AssessorID); err == nil && assessor != nil {
			detail.AssessorName = strings.TrimSpace(assessor.FirstName + " " + assessor.LastName)
		}
	}
	if appointment.CentreID != "" {
		if centre, err := GetCentre(ctx, appointment.CentreID); err == nil && centre != nil {
			detail.CentreName = centre.Name
			detail.CentreAddress = centre.Address
		}
	}
	if appointment.PracticalTaskID != "" {
		if task, err := GetPracticalTask(ctx, appointment.PracticalTaskID); err == nil && task != nil {
			detail.TaskName = task.Name
		}
	}
	// The slot carries the end time; the appointment only stores the start.
	if appointment.SlotID != "" {
		if slot, err := GetSlotByID(ctx, appointment.SlotID); err == nil && slot != nil {
			ends := slot.EndsAt.UTC().Format(time.RFC3339)
			detail.EndsAt = &ends
		}
	}

	detail.CanReschedule, detail.RescheduleBlockedBy = rescheduleEligibility(appointment, time.Now().UTC())
	return responses.SendOK(c, detail, "Appointment retrieved")
}

// rescheduleEligibility centralises the cancel-to-rebook rule so the read and
// write paths cannot disagree about it.
func rescheduleEligibility(a *Appointment, now time.Time) (bool, string) {
	if a.Status != StatusScheduled {
		return false, "This appointment is no longer scheduled."
	}
	if a.DecidedAt != nil {
		return false, "This appointment has already been assessed."
	}
	if !a.ScheduledFor.After(now) {
		return false, "This appointment has already started."
	}
	if a.ScheduledFor.Sub(now) < rescheduleCutoff {
		return false, "Rescheduling closes 12 hours before the session. Contact support if you cannot attend."
	}
	return true, ""
}

// HandleCancelAppointment cancels an artisan's own appointment so they can book
// a different slot. The slot is released and the stage returns to available.
func HandleCancelAppointment(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	appointmentID := strings.TrimSpace(c.QueryParam("id"))
	if appointmentID == "" {
		return responses.SendError(c, http.StatusBadRequest, "id is required")
	}
	ctx := c.Request().Context()
	appointment, err := GetAppointmentForUser(ctx, appointmentID, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "appointment not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load appointment")
	}
	if ok, reason := rescheduleEligibility(appointment, time.Now().UTC()); !ok {
		return responses.SendError(c, http.StatusBadRequest, reason)
	}

	journey, err := GetJourneyByID(ctx, appointment.JourneyID)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load journey")
	}

	// Ordered so that every failure point leaves a state the artisan can act on.
	//
	// The journey goes first because it is the only step with no compensating
	// action: if it fails, nothing has changed and the artisan still holds a
	// valid booking. Releasing the slot first would risk the reverse — a stage
	// stuck on "scheduled" with the slot already given away, which is
	// unrecoverable without manual intervention.
	//
	// After it succeeds the remaining steps are cleanup. If either fails the
	// stage already reads "available", so the artisan can rebook; the residue is
	// a slot that stays marked booked (lost capacity, not a broken journey) and
	// an appointment row no longer reachable through the active-appointment
	// query, which filters on decided_at and the stage status.
	now := time.Now().UTC()
	state := journey.Stages[appointment.Stage]
	state.Status = StatusAvailable
	state.UpdatedAt = now
	journey.Stages[appointment.Stage] = state
	if err := SaveJourneyStages(ctx, journey); err != nil {
		slog.Error("failed to reset stage on cancel; booking left intact",
			"journey_id", journey.ID, "appointment_id", appointment.ID, "error", err)
		return responses.SendError(c, http.StatusInternalServerError, "failed to cancel the appointment")
	}
	if err := ReleaseSlot(ctx, appointment.SlotID); err != nil {
		// Logged at error because it silently costs the assessor a bookable slot
		// and needs an operator to reconcile.
		slog.Error("stage reset but slot not released; slot capacity leaked",
			"slot_id", appointment.SlotID, "appointment_id", appointment.ID, "error", err)
	}
	if err := DeleteAppointment(ctx, appointment.ID); err != nil {
		slog.Error("stage reset but appointment not deleted",
			"appointment_id", appointment.ID, "error", err)
	}

	slog.Info("appointment cancelled for reschedule", "appointment_id", appointment.ID, "user_id", userID, "stage", appointment.Stage)
	notifyOps(ctx, "Assessment Cancelled", fmt.Sprintf("User %s cancelled their *%s* appointment scheduled for %s.", userID, string(appointment.Stage), appointment.ScheduledFor.Format("Mon 02 Jan 15:04")))
	return responses.SendOK(c, nil, "Appointment cancelled. You can now book a new slot.")
}

// HandleListPracticalTasks returns the active practical tasks an artisan can
// choose from when booking a practical appointment. Unlike the admin listing it
// never exposes inactive tasks, and skill_id is required: an artisan browsing
// every skill's tasks has no use case and would leak the full task catalogue.
func HandleListPracticalTasks(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	skillID := strings.TrimSpace(c.QueryParam("skill_id"))
	if skillID == "" {
		return responses.SendError(c, http.StatusBadRequest, "skill_id is required")
	}
	tasks, err := ListPracticalTasks(c.Request().Context(), skillID)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list practical tasks")
	}
	return responses.SendOK(c, tasks, "Practical tasks retrieved")
}

// HandleListCentres returns active centres for an artisan choosing a practical
// venue. location_ids is an optional comma-separated narrowing filter; omitting
// it returns every active centre rather than none, matching ListCentres.
func HandleListCentres(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	locationIDs := []string{}
	for id := range strings.SplitSeq(c.QueryParam("location_ids"), ",") {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			locationIDs = append(locationIDs, trimmed)
		}
	}
	centres, err := ListCentres(c.Request().Context(), locationIDs)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list centres")
	}
	return responses.SendOK(c, centres, "Centres retrieved")
}

// HandleGetCertificate returns the artisan's own certificate for a journey.
// The certificate's user_id is checked against the caller so one artisan cannot
// read another's certificate by guessing a journey id; a mismatch is reported as
// not-found rather than forbidden so the endpoint does not confirm that a
// journey id exists for somebody else.
func HandleGetCertificate(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	journeyID := c.Param("id")
	if strings.TrimSpace(journeyID) == "" {
		return responses.SendError(c, http.StatusBadRequest, "journey id is required")
	}
	cert, err := GetCertificateByJourney(c.Request().Context(), journeyID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "certificate not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load certificate")
	}
	if cert.UserID != userID {
		return responses.SendError(c, http.StatusNotFound, "certificate not found")
	}
	return responses.SendOK(c, cert, "Certificate retrieved")
}

type bookAppointmentRequest struct {
	SlotID          string `json:"slot_id"`
	PracticalTaskID string `json:"practical_task_id,omitempty"`
}

func HandleBookAppointment(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	var req bookAppointmentRequest
	if err := c.Bind(&req); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	if strings.TrimSpace(req.SlotID) == "" {
		return responses.SendError(c, http.StatusBadRequest, "slot_id is required")
	}
	ctx := c.Request().Context()
	journey, err := GetJourneyForUser(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "no journey found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load journey")
	}
	slot, err := ClaimSlot(ctx, req.SlotID, userID)
	if err != nil {
		if errors.Is(err, ErrSlotUnavailable) {
			return responses.SendError(c, http.StatusConflict, err.Error())
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to claim slot")
	}
	if slot.Stage == StagePractical && strings.TrimSpace(req.PracticalTaskID) == "" {
		_ = ReleaseSlot(ctx, slot.ID)
		return responses.SendError(c, http.StatusBadRequest, ErrPracticalTaskNeeded.Error())
	}
	appointment := &Appointment{
		JourneyID:       journey.ID,
		UserID:          userID,
		Stage:           slot.Stage,
		AssessorID:      slot.AssessorID,
		SlotID:          slot.ID,
		Status:          StatusScheduled,
		CentreID:        slot.CentreID,
		PracticalTaskID: req.PracticalTaskID,
		ScheduledFor:    slot.StartsAt,
	}
	// Create a video meeting for oral appointments so links are ready at join
	// time. A failure here never blocks the booking: the appointment is still
	// valid and the assessor can fall back to another channel.
	if slot.Stage == StageOral {
		if meetingCreator == nil {
			appointment.MeetingStatus = MeetingPending
		} else if meeting, err := meetingCreator.CreateMeeting(
			"Oral Assessment", slot.StartsAt, int(slot.EndsAt.Sub(slot.StartsAt).Minutes()),
		); err != nil {
			// Recorded on the appointment, not just logged, so the artisan is told
			// the link is missing and ops can find the bookings that need fixing.
			appointment.MeetingStatus = MeetingFailed
			slog.Error("meeting creation failed; appointment has no join link",
				"error", err, "slot_id", req.SlotID, "user_id", userID)
			notifyOps(ctx, "Meeting Link Missing", fmt.Sprintf(
				"Could not create a video call for user %s's oral assessment on %s. The booking stands but has no join link.",
				userID, slot.StartsAt.Format("Mon 02 Jan 15:04")))
		} else {
			appointment.MeetingStatus = MeetingReady
			appointment.MeetingKey = meeting.MeetingKey
			appointment.JoinLink = meeting.JoinLink
			appointment.StartLink = meeting.StartLink
		}
	}
	if err := CreateAppointment(ctx, appointment); err != nil {
		_ = ReleaseSlot(ctx, slot.ID)
		slog.Error("failed to create appointment", "user_id", userID, "slot_id", req.SlotID, "error", err)
		return responses.SendError(c, http.StatusInternalServerError, "failed to create appointment")
	}
	now := time.Now().UTC()
	state := journey.Stages[slot.Stage]
	state.Status = StatusScheduled
	state.UpdatedAt = now
	journey.Stages[slot.Stage] = state
	if err := SaveJourneyStages(ctx, journey); err != nil {
		// Without this the journey still reads "available" while a booking exists,
		// so the artisan can book a second slot for the same stage. Undo the
		// booking rather than leave that mismatch behind.
		_ = DeleteAppointment(ctx, appointment.ID)
		_ = ReleaseSlot(ctx, slot.ID)
		slog.Error("failed to advance journey after booking; booking rolled back",
			"journey_id", journey.ID, "appointment_id", appointment.ID, "error", err)
		return responses.SendError(c, http.StatusInternalServerError, "failed to confirm the booking")
	}
	slog.Info("appointment booked", "appointment_id", appointment.ID, "user_id", userID, "stage", slot.Stage, "scheduled_for", slot.StartsAt)
	notifyOps(ctx, "Assessment Booked", fmt.Sprintf("User %s booked *%s* appointment for %s.", userID, string(slot.Stage), slot.StartsAt.Format("Mon 02 Jan 15:04")))
	return responses.SendCreated(c, appointment, "Appointment booked")
}

// --- Assessor handlers ---

func HandleGetDashboard(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	ctx := c.Request().Context()
	now := time.Now().UTC()
	appointments, err := ListAppointmentsForAssessor(ctx, userID, now)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load appointments")
	}
	type dashboard struct {
		Appointments []Appointment `json:"appointments"`
	}
	return responses.SendOK(c, dashboard{Appointments: appointments}, "Dashboard loaded")
}

func HandleListAppointments(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	now := time.Now().UTC()
	appointments, err := ListAppointmentsForAssessor(c.Request().Context(), userID, now)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list appointments")
	}
	return responses.SendOK(c, appointments, "Appointments retrieved")
}

func HandleGetAppointment(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	id := c.Param("id")
	appointment, err := GetAppointmentByID(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "appointment not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load appointment")
	}
	return responses.SendOK(c, appointment, "Appointment retrieved")
}

func HandleJoinCall(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	id := c.Param("id")
	appointment, err := GetAppointmentForAssessor(c.Request().Context(), id, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "appointment not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load appointment")
	}
	if appointment.Status == StatusScheduled {
		_ = UpdateAppointment(c.Request().Context(), id, bson.M{"status": StatusInProgress})
	}
	// Return meeting links explicitly since StartLink is json:"-" on the model.
	type meetingLinks struct {
		MeetingKey string `json:"meeting_key"`
		JoinLink   string `json:"join_link"`
		StartLink  string `json:"start_link"`
	}
	links := meetingLinks{
		MeetingKey: appointment.MeetingKey,
		JoinLink:   appointment.JoinLink,
		StartLink:  appointment.StartLink,
	}
	return responses.SendOK(c, links, "Joined call")
}

func HandleNotifyArtisan(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	id := c.Param("id")
	_, err := GetAppointmentForAssessor(c.Request().Context(), id, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "appointment not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load appointment")
	}
	return responses.SendOK(c, nil, "Artisan notified")
}

type submitReportRequest struct {
	Score          int    `json:"score"`
	MaxScore       int    `json:"max_score"`
	Notes          string `json:"notes"`
	Feedback       string `json:"feedback"`
	EvidenceFileID string `json:"evidence_file_id,omitempty"`
}

func HandleSubmitReport(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	id := c.Param("id")
	var req submitReportRequest
	if err := c.Bind(&req); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	if err := ValidateReport(req.Score, req.MaxScore, req.Notes); err != nil {
		return responses.SendError(c, http.StatusBadRequest, err.Error())
	}
	ctx := c.Request().Context()
	appointment, err := GetAppointmentForAssessor(ctx, id, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "appointment not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load appointment")
	}
	now := time.Now().UTC()
	session := &Session{
		AppointmentID: appointment.ID,
		AssessorID:    userID,
		UserID:        appointment.UserID,
		Stage:         appointment.Stage,
		Score:         req.Score,
		MaxScore:      req.MaxScore,
		Notes:         req.Notes,
		Feedback:      req.Feedback,
		StartedAt:     appointment.ScheduledFor,
		EndedAt:       now,
	}
	if req.EvidenceFileID != "" {
		session.EvidenceFiles = []string{req.EvidenceFileID}
	}
	if err := CreateSession(ctx, session); err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to create session")
	}
	updates := bson.M{
		"score":          req.Score,
		"max_score":      req.MaxScore,
		"assessor_notes": req.Notes,
		"decided_at":     now,
		"status":         StatusPassed,
	}
	if req.EvidenceFileID != "" {
		updates["evidence_file_id"] = req.EvidenceFileID
	}
	passPercent := WrittenPassPercent
	if appointment.Stage == StagePractical {
		passPercent = PracticalPassPercent
	}
	passed := Passed(req.Score, req.MaxScore, passPercent)
	if !passed {
		updates["status"] = StatusFailed
	}
	if err := UpdateAppointment(ctx, id, updates); err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to update appointment")
	}
	journey, err := GetJourneyByID(ctx, appointment.JourneyID)
	if err == nil {
		_ = ApplyDecision(journey, appointment.Stage, passed, userID, req.Notes, req.Feedback, now)
		state := journey.Stages[appointment.Stage]
		state.Score = req.Score
		state.MaxScore = req.MaxScore
		journey.Stages[appointment.Stage] = state
		_ = SaveJourneyStages(ctx, journey)
	}
	slog.Info("assessment report submitted",
		"appointment_id", id,
		"assessor_id", userID,
		"stage", appointment.Stage,
		"score", req.Score,
		"max_score", req.MaxScore,
		"passed", passed)
	return responses.SendOK(c, session, "Report submitted")
}

func HandleListSlots(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	slots, err := ListSlotsForAssessor(c.Request().Context(), userID)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list slots")
	}
	return responses.SendOK(c, slots, "Slots retrieved")
}

func HandleCreateSlot(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	var slot Slot
	if err := c.Bind(&slot); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	slot.AssessorID = userID
	if err := ValidateSlot(slot); err != nil {
		return responses.SendError(c, http.StatusBadRequest, err.Error())
	}
	if err := CreateSlot(c.Request().Context(), &slot); err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to create slot")
	}
	return responses.SendCreated(c, slot, "Slot created")
}

type generateSlotsRequest struct {
	Stage    Stage     `json:"stage"`
	CentreID string    `json:"centre_id,omitempty"`
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
	Duration int       `json:"duration_minutes"`
}

func HandleGenerateSlots(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	var req generateSlotsRequest
	if err := c.Bind(&req); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	if req.Duration <= 0 {
		req.Duration = 60
	}
	ctx := c.Request().Context()
	duration := time.Duration(req.Duration) * time.Minute
	created := []Slot{}
	current := req.StartsAt
	for current.Add(duration).Before(req.EndsAt) || current.Add(duration).Equal(req.EndsAt) {
		slot := Slot{
			AssessorID: userID,
			Stage:      req.Stage,
			CentreID:   req.CentreID,
			StartsAt:   current,
			EndsAt:     current.Add(duration),
		}
		if err := ValidateSlot(slot); err != nil {
			return responses.SendError(c, http.StatusBadRequest, err.Error())
		}
		if err := CreateSlot(ctx, &slot); err != nil {
			return responses.SendError(c, http.StatusInternalServerError, "failed to create slot")
		}
		created = append(created, slot)
		current = current.Add(duration)
	}
	return responses.SendCreated(c, created, "Slots generated")
}

func HandleDeleteSlot(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	id := c.Param("id")
	if err := DeleteSlot(c.Request().Context(), id, userID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "slot not found or already booked")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to delete slot")
	}
	return responses.SendOK(c, nil, "Slot deleted")
}

func HandleGetRubric(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	skillID := c.QueryParam("skill_id")
	if skillID == "" {
		return responses.SendError(c, http.StatusBadRequest, "skill_id is required")
	}
	questions, err := ListQuestionsBySkill(c.Request().Context(), skillID, QuestionApproved)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load rubric")
	}
	return responses.SendOK(c, questions, "Rubric retrieved")
}

func HandleListOwnSkills(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	skills, err := ListAssessorSkillsByAssessor(c.Request().Context(), userID)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list skills")
	}
	return responses.SendOK(c, skills, "Skills retrieved")
}

// --- Admin handlers ---

func HandleListReviewQueue(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	stage := Stage(c.QueryParam("stage"))
	if stage == "" {
		stage = StageProfileReview
	}
	journeys, err := ListJourneysByStage(c.Request().Context(), stage, 50)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list review queue")
	}
	return responses.SendOK(c, journeys, "Review queue retrieved")
}

func HandleApproveProfile(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	id := c.Param("id")
	ctx := c.Request().Context()
	journey, err := GetJourneyByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "journey not found")
		}
		slog.Error("failed to load journey for approval", "journey_id", id, "error", err)
		return responses.SendError(c, http.StatusInternalServerError, "failed to load journey")
	}
	now := time.Now().UTC()
	if err := ApplyDecision(journey, StageProfileReview, true, userID, "", "", now); err != nil {
		return responses.SendError(c, http.StatusBadRequest, err.Error())
	}
	if err := SaveJourneyStages(ctx, journey); err != nil {
		slog.Error("failed to save journey stages after approval", "journey_id", id, "error", err)
		return responses.SendError(c, http.StatusInternalServerError, "failed to save journey")
	}
	slog.Info("profile approved", "journey_id", id, "user_id", journey.UserID, "approved_by", userID)
	notifyOps(ctx, "Profile Approved", fmt.Sprintf("Journey *%s* (user %s) profile approved by admin %s.", id, journey.UserID, userID))
	return responses.SendOK(c, journey, "Profile approved")
}

type rejectProfileRequest struct {
	Notes    string `json:"notes"`
	Feedback string `json:"feedback"`
}

func HandleRejectProfile(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	id := c.Param("id")
	var req rejectProfileRequest
	if err := c.Bind(&req); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	ctx := c.Request().Context()
	journey, err := GetJourneyByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "journey not found")
		}
		slog.Error("failed to load journey for rejection", "journey_id", id, "error", err)
		return responses.SendError(c, http.StatusInternalServerError, "failed to load journey")
	}
	now := time.Now().UTC()
	if err := ApplyDecision(journey, StageProfileReview, false, userID, req.Notes, req.Feedback, now); err != nil {
		return responses.SendError(c, http.StatusBadRequest, err.Error())
	}
	if err := SaveJourneyStages(ctx, journey); err != nil {
		slog.Error("failed to save journey stages after rejection", "journey_id", id, "error", err)
		return responses.SendError(c, http.StatusInternalServerError, "failed to save journey")
	}
	slog.Info("profile rejected", "journey_id", id, "user_id", journey.UserID, "rejected_by", userID)
	notifyOps(ctx, "Profile Rejected", fmt.Sprintf("Journey *%s* (user %s) profile rejected by admin %s.", id, journey.UserID, userID))
	return responses.SendOK(c, journey, "Profile rejected")
}

func HandleListAssessorSkills(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	skills, err := ListAllAssessorSkills(c.Request().Context())
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list assessor skills")
	}
	return responses.SendOK(c, skills, "Assessor skills retrieved")
}

type grantSkillsRequest struct {
	AssessorID string   `json:"assessor_id"`
	SkillIDs   []string `json:"skill_ids"`
}

func HandleGrantSkills(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	var req grantSkillsRequest
	if err := c.Bind(&req); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	if strings.TrimSpace(req.AssessorID) == "" || len(req.SkillIDs) == 0 {
		return responses.SendError(c, http.StatusBadRequest, "assessor_id and skill_ids are required")
	}
	ctx := c.Request().Context()
	now := time.Now().UTC()
	granted := []AssessorSkill{}
	for _, skillID := range req.SkillIDs {
		skill := &AssessorSkill{
			AssessorID: req.AssessorID,
			SkillID:    skillID,
			GrantedBy:  userID,
			GrantedAt:  now,
		}
		if err := CreateAssessorSkill(ctx, skill); err != nil {
			continue
		}
		granted = append(granted, *skill)
	}
	return responses.SendCreated(c, granted, "Skills granted")
}

func HandleRevokeSkill(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	id := c.Param("id")
	if err := DeleteAssessorSkill(c.Request().Context(), id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "skill not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to revoke skill")
	}
	return responses.SendOK(c, nil, "Skill revoked")
}

type reassignRequest struct {
	AppointmentID string `json:"appointment_id"`
	AssessorID    string `json:"assessor_id"`
}

func HandleReassign(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	var req reassignRequest
	if err := c.Bind(&req); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	if strings.TrimSpace(req.AppointmentID) == "" || strings.TrimSpace(req.AssessorID) == "" {
		return responses.SendError(c, http.StatusBadRequest, "appointment_id and assessor_id are required")
	}
	ctx := c.Request().Context()
	if err := UpdateAppointment(ctx, req.AppointmentID, bson.M{"assessor_id": req.AssessorID}); err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "appointment not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to reassign")
	}
	return responses.SendOK(c, nil, "Appointment reassigned")
}

// --- Admin question bank handlers ---

// AdminQuestion is a response struct that exposes the answer key (correct_option_ids
// and rubric) to admin users, unlike the base Question struct which uses json:"-".
type AdminQuestion struct {
	ID               string           `json:"id"`
	SkillID          string           `json:"skill_id"`
	Type             QuestionType     `json:"type"`
	Prompt           string           `json:"prompt"`
	Options          []QuestionOption `json:"options,omitempty"`
	CorrectOptionIDs []string         `json:"correct_option_ids,omitempty"`
	Rubric           string           `json:"rubric,omitempty"`
	Points           int              `json:"points"`
	Status           QuestionStatus   `json:"status"`
	Source           string           `json:"source"`
	CreatedBy        string           `json:"created_by,omitempty"`
	ApprovedBy       string           `json:"approved_by,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

func toAdminQuestion(q Question) AdminQuestion {
	return AdminQuestion(q)
}

// parsePageLimit reads the shared admin pagination parameters, clamping limit so
// a caller cannot request an unbounded page.
func parsePageLimit(c echo.Context) (page, limit int64) {
	page, limit = 1, defaultAdminPageLimit
	if p := c.QueryParam("page"); p != "" {
		if v, err := strconv.ParseInt(p, 10, 64); err == nil && v > 0 {
			page = v
		}
	}
	if l := c.QueryParam("limit"); l != "" {
		if v, err := strconv.ParseInt(l, 10, 64); err == nil && v > 0 && v <= maxAdminPageLimit {
			limit = v
		}
	}
	return page, limit
}

// adminPage is the envelope for every paginated admin list.
type adminPage[T any] struct {
	Data  []T   `json:"data"`
	Total int64 `json:"total"`
	Page  int64 `json:"page"`
	Limit int64 `json:"limit"`
}

const (
	defaultAdminPageLimit = 20
	maxAdminPageLimit     = 100
)

func HandleAdminListQuestions(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	skillID := c.QueryParam("skill_id")
	status := QuestionStatus(c.QueryParam("status"))
	page, limit := parsePageLimit(c)
	questions, total, err := ListQuestionsAdmin(c.Request().Context(), skillID, status, page, limit)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list questions")
	}
	result := make([]AdminQuestion, 0, len(questions))
	for _, q := range questions {
		result = append(result, toAdminQuestion(q))
	}
	return responses.SendOK(c, adminPage[AdminQuestion]{
		Data:  result,
		Total: total,
		Page:  page,
		Limit: limit,
	}, "Questions retrieved")
}

type createQuestionRequest struct {
	SkillID          string           `json:"skill_id"`
	Type             QuestionType     `json:"type"`
	Prompt           string           `json:"prompt"`
	Options          []QuestionOption `json:"options"`
	CorrectOptionIDs []string         `json:"correct_option_ids"`
	Rubric           string           `json:"rubric"`
	Points           int              `json:"points"`
}

func HandleAdminCreateQuestion(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	var req createQuestionRequest
	if err := c.Bind(&req); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	if strings.TrimSpace(req.SkillID) == "" {
		return responses.SendError(c, http.StatusBadRequest, "skill_id is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return responses.SendError(c, http.StatusBadRequest, "prompt is required")
	}
	if req.Points <= 0 {
		return responses.SendError(c, http.StatusBadRequest, "points must be positive")
	}
	validTypes := map[QuestionType]bool{
		QuestionSingleChoice: true,
		QuestionMultiChoice:  true,
		QuestionTrueFalse:    true,
		QuestionFreeText:     true,
	}
	if !validTypes[req.Type] {
		return responses.SendError(c, http.StatusBadRequest, "type must be single_choice, multi_choice, true_false, or free_text")
	}

	// Validate CorrectOptionIDs for non-free-text questions and Rubric for free-text.
	if req.Type != QuestionFreeText {
		if len(req.CorrectOptionIDs) == 0 {
			return responses.SendError(c, http.StatusBadRequest, "correct_option_ids required for non-free-text questions")
		}
	} else {
		if req.Rubric == "" {
			return responses.SendError(c, http.StatusBadRequest, "rubric required for free-text questions")
		}
	}

	question := &Question{
		SkillID:          req.SkillID,
		Type:             req.Type,
		Prompt:           req.Prompt,
		Options:          req.Options,
		CorrectOptionIDs: req.CorrectOptionIDs,
		Rubric:           req.Rubric,
		Points:           req.Points,
		Status:           QuestionDraft,
		Source:           SourceHuman,
		CreatedBy:        userID,
	}
	if err := CreateQuestion(c.Request().Context(), question); err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to create question")
	}
	return responses.SendCreated(c, toAdminQuestion(*question), "Question created")
}

func HandleAdminApproveQuestion(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	id := c.Param("id")
	if err := SetQuestionStatus(c.Request().Context(), id, userID, QuestionApproved); err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "question not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to approve question")
	}
	return responses.SendOK(c, nil, "Question approved")
}

func HandleAdminRetireQuestion(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	id := c.Param("id")
	if err := SetQuestionStatus(c.Request().Context(), id, userID, QuestionRetired); err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "question not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to retire question")
	}
	return responses.SendOK(c, nil, "Question retired")
}

// --- Admin journey handlers ---

func HandleAdminListJourneys(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	stage := Stage(c.QueryParam("stage"))
	status := StageStatus(c.QueryParam("status"))
	page, limit := parsePageLimit(c)

	journeys, total, err := ListAllJourneys(c.Request().Context(), stage, status, page, limit)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list journeys")
	}
	return responses.SendOK(c, adminPage[Journey]{
		Data:  journeys,
		Total: total,
		Page:  page,
		Limit: limit,
	}, "Journeys retrieved")
}

func HandleAdminGetJourney(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	id := c.Param("id")
	journey, err := GetJourneyByID(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return responses.SendError(c, http.StatusNotFound, "journey not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load journey")
	}
	return responses.SendOK(c, journey, "Journey retrieved")
}

// --- Admin centres handlers ---

func HandleAdminListCentres(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	centres, err := ListAllCentres(c.Request().Context())
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list centres")
	}
	return responses.SendOK(c, centres, "Centres retrieved")
}

type createCentreRequest struct {
	Name                 string `json:"name"`
	LocationID           string `json:"location_id"`
	PartnerInstitutionID string `json:"partner_institution_id"`
	Address              string `json:"address"`
	IsActive             bool   `json:"is_active"`
}

func HandleAdminCreateCentre(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	var req createCentreRequest
	if err := c.Bind(&req); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	if strings.TrimSpace(req.Name) == "" {
		return responses.SendError(c, http.StatusBadRequest, "name is required")
	}
	if strings.TrimSpace(req.LocationID) == "" {
		return responses.SendError(c, http.StatusBadRequest, "location_id is required")
	}
	centre := &Centre{
		Name:                 req.Name,
		LocationID:           req.LocationID,
		PartnerInstitutionID: req.PartnerInstitutionID,
		Address:              req.Address,
		IsActive:             req.IsActive,
	}
	if err := CreateCentre(c.Request().Context(), centre); err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to create centre")
	}
	return responses.SendCreated(c, centre, "Centre created")
}

// --- Admin practical tasks handlers ---

func HandleAdminListPracticalTasks(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	skillID := c.QueryParam("skill_id")
	tasks, err := ListAllPracticalTasks(c.Request().Context(), skillID)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list practical tasks")
	}
	return responses.SendOK(c, tasks, "Practical tasks retrieved")
}

type createPracticalTaskRequest struct {
	SkillID      string `json:"skill_id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instruction  string `json:"instruction"`
	WhatToSubmit string `json:"what_to_submit"`
	IsActive     bool   `json:"is_active"`
}

func HandleAdminCreatePracticalTask(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	var req createPracticalTaskRequest
	if err := c.Bind(&req); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	if strings.TrimSpace(req.SkillID) == "" {
		return responses.SendError(c, http.StatusBadRequest, "skill_id is required")
	}
	if strings.TrimSpace(req.Name) == "" {
		return responses.SendError(c, http.StatusBadRequest, "name is required")
	}
	task := &PracticalTask{
		SkillID:      req.SkillID,
		Name:         req.Name,
		Description:  req.Description,
		Instruction:  req.Instruction,
		WhatToSubmit: req.WhatToSubmit,
		IsActive:     req.IsActive,
	}
	if err := CreatePracticalTask(c.Request().Context(), task); err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to create practical task")
	}
	return responses.SendCreated(c, task, "Practical task created")
}

// --- Admin certificates handler ---

func HandleAdminListCertificates(c echo.Context) error {
	userID := contextutil.GetUserID(c)
	if userID == "" {
		return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
	}
	filterUserID := c.QueryParam("user_id")
	certs, err := ListCertificatesByUser(c.Request().Context(), filterUserID)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list certificates")
	}
	return responses.SendOK(c, certs, "Certificates retrieved")
}

// notifyOps sends a lightweight Slack message to the ops notifications channel
// for business-critical assessment events (journey starts, profile decisions,
// bookings). Failures are logged but never surface to the caller.
func notifyOps(ctx context.Context, title, body string) {
	if opsSlackClient == nil {
		return
	}
	msg := integrations.BlockKitMessage{
		Blocks: []integrations.Block{
			{
				Type: "header",
				Text: &integrations.TextObject{Type: "plain_text", Text: title},
			},
			{
				Type: "section",
				Text: &integrations.TextObject{Type: "mrkdwn", Text: body},
			},
		},
	}
	if err := opsSlackClient.Send(ctx, msg); err != nil {
		slog.Warn("failed to send ops notification", "title", title, "error", err)
	}
}
