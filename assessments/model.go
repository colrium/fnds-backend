// Package assessments implements the artisan assessment pipeline: a staged
// journey from a completed profile through an oral interview, a written
// assessment, and a paid practical exam to a certified artisan.
//
// Layering follows the repo convention: this file owns the types and all
// MongoDB access, service.go owns the pure stage/scoring logic (deliberately
// Mongo-free so it is unit-testable), handlers.go owns the Echo handlers.
package assessments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	collectionJourneys       = "assessment_journeys"
	collectionQuestions      = "assessment_questions"
	collectionAttempts       = "assessment_attempts"
	collectionAppointments   = "assessment_appointments"
	collectionPracticalTask  = "assessment_practical_tasks"
	collectionCentres        = "assessment_centres"
	collectionSlots          = "assessment_slots"
	collectionExamPayments   = "assessment_exam_payments"
	collectionAssessorSkills = "assessment_assessor_skills"
	collectionSessions       = "assessment_sessions"
)

// mongoIndexAlreadyExistsCode is returned when an index already exists with the
// same spec. Safe to ignore during startup, matching institutions.EnsureIndexes.
const mongoIndexAlreadyExistsCode = 85

var (
	ErrNotFound            = errors.New("assessment record not found")
	ErrJourneyExists       = errors.New("an assessment journey already exists for this skill")
	ErrAssessorSkillExists = errors.New("this assessor skill grant already exists")
	ErrSlotUnavailable     = errors.New("that slot is no longer available")
	ErrPaymentUnavailable  = errors.New("payments are temporarily unavailable")
)

// Stage is a step in the pipeline. Stages are ordered; see StageOrder.
type Stage string

const (
	StageProfileReview Stage = "profile_review"
	StageOral          Stage = "oral"
	StageWritten       Stage = "written"
	StagePractical     Stage = "practical"
	StageCertified     Stage = "certified"
)

// MeetingStatus records the outcome of provisioning a video call for an oral
// appointment, so an absent join link is never ambiguous.
type MeetingStatus string

const (
	// MeetingReady means a join link was created and stored.
	MeetingReady MeetingStatus = "ready"
	// MeetingPending means the provider is not configured, so no link will
	// appear without operator action. The appointment is still valid.
	MeetingPending MeetingStatus = "pending"
	// MeetingFailed means provisioning was attempted and errored. Ops should
	// retry or arrange another channel before the session.
	MeetingFailed MeetingStatus = "failed"
)

// StageStatus is the state of a single stage within a journey.
type StageStatus string

const (
	// StatusLocked means a prior stage has not been passed yet.
	StatusLocked StageStatus = "locked"
	// StatusAvailable means the artisan may start or book this stage.
	StatusAvailable StageStatus = "available"
	// StatusAwaitingPayment is the practical stage between booking and a
	// confirmed Cellulant webhook.
	StatusAwaitingPayment StageStatus = "awaiting_payment"
	StatusScheduled       StageStatus = "scheduled"
	StatusInProgress      StageStatus = "in_progress"
	// StatusAwaitingReview means submitted and waiting on a human decision.
	StatusAwaitingReview StageStatus = "awaiting_review"
	StatusPassed         StageStatus = "passed"
	StatusFailed         StageStatus = "failed"
)

// StageState is the per-stage record inside a Journey.
type StageState struct {
	Status   StageStatus `json:"status" bson:"status"`
	Score    int         `json:"score,omitempty" bson:"score,omitempty"`
	MaxScore int         `json:"max_score,omitempty" bson:"max_score,omitempty"`
	// Attempts counts completed attempts, used with NextAttemptAt to enforce
	// the written-stage cooldown.
	Attempts      int        `json:"attempts" bson:"attempts"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty" bson:"next_attempt_at,omitempty"`
	// Notes is assessor- or reviewer-authored. It is carried forward onto the
	// next attempt after a failure so the artisan and the next reviewer both
	// keep continuity.
	Notes string `json:"notes,omitempty" bson:"notes,omitempty"`
	// Feedback is artisan-facing, plain-language guidance (AI or human).
	Feedback  string     `json:"feedback,omitempty" bson:"feedback,omitempty"`
	DecidedBy string     `json:"decided_by,omitempty" bson:"decided_by,omitempty"`
	DecidedAt *time.Time `json:"decided_at,omitempty" bson:"decided_at,omitempty"`
	UpdatedAt time.Time  `json:"updated_at" bson:"updated_at"`
}

// Journey is the single document that answers "where is this artisan" in one
// read. One per artisan per skill. The profile wizard requires exactly one
// skill, so in practice this is one per artisan today, but the skill is
// recorded so a second trade does not need a schema change.
type Journey struct {
	ID           string               `json:"id" bson:"_id"`
	UserID       string               `json:"user_id" bson:"user_id"`
	SkillID      string               `json:"skill_id" bson:"skill_id"`
	CurrentStage Stage                `json:"current_stage" bson:"current_stage"`
	Stages       map[Stage]StageState `json:"stages" bson:"stages"`
	CreatedAt    time.Time            `json:"created_at" bson:"created_at"`
	UpdatedAt    time.Time            `json:"updated_at" bson:"updated_at"`
}

// QuestionType determines how an answer is scored. Everything except
// QuestionFreeText is scored deterministically in Go; free text goes to Bedrock.
type QuestionType string

const (
	QuestionSingleChoice QuestionType = "single_choice"
	QuestionMultiChoice  QuestionType = "multi_choice"
	QuestionTrueFalse    QuestionType = "true_false"
	QuestionFreeText     QuestionType = "free_text"
)

// QuestionStatus gates a question into the live bank. AI-drafted questions land
// in QuestionDraft and require human approval before they can be drawn.
type QuestionStatus string

const (
	QuestionDraft    QuestionStatus = "draft"
	QuestionApproved QuestionStatus = "approved"
	QuestionRetired  QuestionStatus = "retired"
)

// QuestionSource records provenance so an AI-authored bank can be audited.
const (
	SourceHuman   = "human"
	SourceAIDraft = "ai_draft"
)

type QuestionOption struct {
	ID   string `json:"id" bson:"id"`
	Text string `json:"text" bson:"text"`
}

// Question is a bank entry.
//
// CorrectOptionIDs and Rubric are `json:"-"` on purpose: the answer key must
// never reach a client. Enforcing it at the type level means a future handler
// cannot leak it by accident, which is the whole basis of the anti-cheat
// approach (a leaked key makes the bank worthless immediately).
type Question struct {
	ID               string           `json:"id" bson:"_id"`
	SkillID          string           `json:"skill_id" bson:"skill_id"`
	Type             QuestionType     `json:"type" bson:"type"`
	Prompt           string           `json:"prompt" bson:"prompt"`
	Options          []QuestionOption `json:"options,omitempty" bson:"options,omitempty"`
	CorrectOptionIDs []string         `json:"-" bson:"correct_option_ids,omitempty"`
	Rubric           string           `json:"-" bson:"rubric,omitempty"`
	Points           int              `json:"points" bson:"points"`
	Status           QuestionStatus   `json:"status" bson:"status"`
	Source           string           `json:"source" bson:"source"`
	CreatedBy        string           `json:"created_by,omitempty" bson:"created_by,omitempty"`
	ApprovedBy       string           `json:"approved_by,omitempty" bson:"approved_by,omitempty"`
	CreatedAt        time.Time        `json:"created_at" bson:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at" bson:"updated_at"`
}

type AttemptStatus string

const (
	AttemptInProgress     AttemptStatus = "in_progress"
	AttemptPaused         AttemptStatus = "paused"
	AttemptSubmitted      AttemptStatus = "submitted"
	AttemptScored         AttemptStatus = "scored"
	AttemptAwaitingReview AttemptStatus = "awaiting_review"
)

// AttemptQuestion is a frozen, per-attempt copy of a bank question with its
// options already shuffled. Snapshotting matters twice over: editing or
// retiring a bank question must not retroactively change a submitted attempt's
// grade, and the shuffled order has to be stable across pause/resume.
type AttemptQuestion struct {
	QuestionID       string           `json:"question_id" bson:"question_id"`
	Type             QuestionType     `json:"type" bson:"type"`
	Prompt           string           `json:"prompt" bson:"prompt"`
	Options          []QuestionOption `json:"options,omitempty" bson:"options,omitempty"`
	Points           int              `json:"points" bson:"points"`
	CorrectOptionIDs []string         `json:"-" bson:"correct_option_ids,omitempty"`
	Rubric           string           `json:"-" bson:"rubric,omitempty"`
}

// AttemptAnswer is one autosaved answer.
//
// PasteCount, BlurCount and DwellMS are integrity signals surfaced to the
// assessor as flags. They deliberately do not affect scoring: an untimed,
// unproctored test cannot be made cheat-proof, and auto-failing on these
// would punish ordinary behaviour (copying a long answer, taking a call).
type AttemptAnswer struct {
	QuestionID string    `json:"question_id" bson:"question_id"`
	OptionIDs  []string  `json:"option_ids,omitempty" bson:"option_ids,omitempty"`
	Text       string    `json:"text,omitempty" bson:"text,omitempty"`
	AnsweredAt time.Time `json:"answered_at" bson:"answered_at"`
	PasteCount int       `json:"paste_count,omitempty" bson:"paste_count,omitempty"`
	BlurCount  int       `json:"blur_count,omitempty" bson:"blur_count,omitempty"`
	DwellMS    int64     `json:"dwell_ms,omitempty" bson:"dwell_ms,omitempty"`
}

// Attempt is one pass at the written assessment. Attempts are retained
// permanently; a re-attempt creates a new document rather than mutating the
// old one, so the history survives.
type Attempt struct {
	ID        string            `json:"id" bson:"_id"`
	JourneyID string            `json:"journey_id" bson:"journey_id"`
	UserID    string            `json:"user_id" bson:"user_id"`
	SkillID   string            `json:"skill_id" bson:"skill_id"`
	Number    int               `json:"number" bson:"number"`
	Status    AttemptStatus     `json:"status" bson:"status"`
	Questions []AttemptQuestion `json:"questions" bson:"questions"`
	// Answers is keyed by question ID so an autosave is an idempotent upsert
	// of a single map entry rather than a read-modify-write of a slice.
	Answers  map[string]AttemptAnswer `json:"answers" bson:"answers"`
	Score    int                      `json:"score" bson:"score"`
	MaxScore int                      `json:"max_score" bson:"max_score"`
	Passed   bool                     `json:"passed" bson:"passed"`
	Feedback string                   `json:"feedback,omitempty" bson:"feedback,omitempty"`
	// AssessorReport is read by the oral/practical assessor before the session.
	AssessorReport string `json:"-" bson:"assessor_report,omitempty"`
	// CarriedNotes is the previous attempt's feedback, carried forward.
	CarriedNotes string `json:"carried_notes,omitempty" bson:"carried_notes,omitempty"`
	// ScoredBy is "auto", "bedrock", or an assessor user ID.
	ScoredBy string `json:"scored_by,omitempty" bson:"scored_by,omitempty"`
	// AuditPrompt/AuditResponse persist the Bedrock exchange so a disputed
	// score can be reconstructed after the fact.
	AuditPrompt   string     `json:"-" bson:"audit_prompt,omitempty"`
	AuditResponse string     `json:"-" bson:"audit_response,omitempty"`
	PauseCount    int        `json:"pause_count" bson:"pause_count"`
	PausedAt      *time.Time `json:"paused_at,omitempty" bson:"paused_at,omitempty"`
	ResumedAt     *time.Time `json:"resumed_at,omitempty" bson:"resumed_at,omitempty"`
	SubmittedAt   *time.Time `json:"submitted_at,omitempty" bson:"submitted_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at" bson:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" bson:"updated_at"`
}

// PracticalTask is a task type an artisan chooses to be assessed on in the
// physical exam ("pick a question").
type PracticalTask struct {
	ID           string    `json:"id" bson:"_id"`
	SkillID      string    `json:"skill_id" bson:"skill_id"`
	Name         string    `json:"name" bson:"name"`
	Description  string    `json:"description" bson:"description"`
	Instruction  string    `json:"instruction" bson:"instruction"`
	WhatToSubmit string    `json:"what_to_submit" bson:"what_to_submit"`
	IsActive     bool      `json:"is_active" bson:"is_active"`
	CreatedAt    time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" bson:"updated_at"`
}

// Centre is a physical verification venue.
//
// This is deliberately not institutions.Institution: that collection is the
// "where did you train" lookup for the profile wizard and carries no location,
// so it cannot answer "centres near me".
type Centre struct {
	ID                   string    `json:"id" bson:"_id"`
	Name                 string    `json:"name" bson:"name"`
	LocationID           string    `json:"location_id" bson:"location_id"`
	PartnerInstitutionID string    `json:"partner_institution_id,omitempty" bson:"partner_institution_id,omitempty"`
	Address              string    `json:"address,omitempty" bson:"address,omitempty"`
	IsActive             bool      `json:"is_active" bson:"is_active"`
	CreatedAt            time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt            time.Time `json:"updated_at" bson:"updated_at"`
}

// Slot is assessor availability. Oral slots have no centre; practical slots do.
type Slot struct {
	ID         string    `json:"id" bson:"_id"`
	AssessorID string    `json:"assessor_id" bson:"assessor_id"`
	Stage      Stage     `json:"stage" bson:"stage"`
	CentreID   string    `json:"centre_id,omitempty" bson:"centre_id,omitempty"`
	Capacity   int       `json:"capacity" bson:"capacity"`
	StartsAt   time.Time `json:"starts_at" bson:"starts_at"`
	EndsAt     time.Time `json:"ends_at" bson:"ends_at"`
	IsBooked   bool      `json:"is_booked" bson:"is_booked"`
	BookedBy   string    `json:"booked_by,omitempty" bson:"booked_by,omitempty"`
	CreatedAt  time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt  time.Time `json:"updated_at" bson:"updated_at"`
}

// Appointment is a booked oral or practical session.
type Appointment struct {
	ID         string      `json:"id" bson:"_id"`
	JourneyID  string      `json:"journey_id" bson:"journey_id"`
	UserID     string      `json:"user_id" bson:"user_id"`
	Stage      Stage       `json:"stage" bson:"stage"`
	AssessorID string      `json:"assessor_id" bson:"assessor_id"`
	SlotID     string      `json:"slot_id" bson:"slot_id"`
	Status     StageStatus `json:"status" bson:"status"`
	// Zoho Meeting fields (oral stage). Created at booking time.
	MeetingKey string `json:"meeting_key,omitempty" bson:"meeting_key,omitempty"`
	JoinLink   string `json:"join_link,omitempty" bson:"join_link,omitempty"`
	// MeetingStatus distinguishes "the link is not ready yet" from "creating it
	// failed". Without it an empty JoinLink is indistinguishable from a pending
	// one, so neither the artisan nor ops learns that the call has no room.
	MeetingStatus MeetingStatus `json:"meeting_status,omitempty" bson:"meeting_status,omitempty"`
	// StartLink is assessor-only; json:"-" ensures it never reaches an artisan client.
	StartLink string `json:"-" bson:"start_link,omitempty"`
	// Practical-only fields.
	CentreID        string    `json:"centre_id,omitempty" bson:"centre_id,omitempty"`
	PracticalTaskID string    `json:"practical_task_id,omitempty" bson:"practical_task_id,omitempty"`
	ScheduledFor    time.Time `json:"scheduled_for" bson:"scheduled_for"`
	// Scoring, filled in by the assessor.
	Score          int        `json:"score,omitempty" bson:"score,omitempty"`
	MaxScore       int        `json:"max_score,omitempty" bson:"max_score,omitempty"`
	AssessorNotes  string     `json:"assessor_notes,omitempty" bson:"assessor_notes,omitempty"`
	EvidenceFileID string     `json:"evidence_file_id,omitempty" bson:"evidence_file_id,omitempty"`
	DecidedAt      *time.Time `json:"decided_at,omitempty" bson:"decided_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at" bson:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at" bson:"updated_at"`
}

// ExamPayment is the 1000 KES practical exam fee. Mirrors the shape of
// subscriptions.UserSubscription's payment fields so the Cellulant webhook
// handling is recognisable to anyone who has read that code.
type ExamPayment struct {
	ID               string    `json:"id" bson:"_id"`
	UserID           string    `json:"user_id" bson:"user_id"`
	JourneyID        string    `json:"journey_id" bson:"journey_id"`
	AppointmentID    string    `json:"appointment_id" bson:"appointment_id"`
	PaymentReference string    `json:"payment_reference" bson:"payment_reference"`
	AmountKES        int64     `json:"amount" bson:"amount"`
	Currency         string    `json:"currency" bson:"currency"`
	Status           string    `json:"status" bson:"status"`
	CreatedAt        time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt        time.Time `json:"updated_at" bson:"updated_at"`
}

// Payment statuses for ExamPayment.
const (
	PaymentPending = "pending"
	PaymentPaid    = "paid"
	PaymentFailed  = "failed"
)

// AssessorSkill records that an assessor is qualified to assess a given skill.
// Used at booking time to match artisans with qualified assessors.
type AssessorSkill struct {
	ID         string    `json:"id" bson:"_id"`
	AssessorID string    `json:"assessor_id" bson:"assessor_id"`
	SkillID    string    `json:"skill_id" bson:"skill_id"`
	GrantedBy  string    `json:"granted_by" bson:"granted_by"`
	GrantedAt  time.Time `json:"granted_at" bson:"granted_at"`
	CreatedAt  time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt  time.Time `json:"updated_at" bson:"updated_at"`
}

// Session is an append-only record of what happened during one oral or
// practical assessment session. One session scores one assessment type.
type Session struct {
	ID            string    `json:"id" bson:"_id"`
	AppointmentID string    `json:"appointment_id" bson:"appointment_id"`
	AssessorID    string    `json:"assessor_id" bson:"assessor_id"`
	UserID        string    `json:"user_id" bson:"user_id"`
	Stage         Stage     `json:"stage" bson:"stage"`
	Score         int       `json:"score" bson:"score"`
	MaxScore      int       `json:"max_score" bson:"max_score"`
	Notes         string    `json:"notes,omitempty" bson:"notes,omitempty"`
	Feedback      string    `json:"feedback,omitempty" bson:"feedback,omitempty"`
	EvidenceFiles []string  `json:"evidence_files,omitempty" bson:"evidence_files,omitempty"`
	StartedAt     time.Time `json:"started_at" bson:"started_at"`
	EndedAt       time.Time `json:"ended_at" bson:"ended_at"`
	CreatedAt     time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" bson:"updated_at"`
}

// EnsureIndexes creates the indexes for every assessments collection. Called
// once at startup from main.go. Idempotent: an "already exists" error is
// treated as success so repeated deploys do not crash.
func EnsureIndexes(ctx context.Context) error {
	specs := []struct {
		collection string
		model      mongo.IndexModel
	}{
		{
			// One journey per artisan per skill. Unique so a double-submit
			// cannot create two parallel pipelines for the same trade.
			collection: collectionJourneys,
			model: mongo.IndexModel{
				Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "skill_id", Value: 1}},
				Options: options.Index().
					SetName("journeys_user_skill_unique").
					SetUnique(true),
			},
		},
		{
			collection: collectionJourneys,
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "current_stage", Value: 1}, {Key: "updated_at", Value: 1}},
				Options: options.Index().SetName("journeys_stage_queue"),
			},
		},
		{
			// Drawing an attempt filters on skill + type + approved status.
			collection: collectionQuestions,
			model: mongo.IndexModel{
				Keys: bson.D{
					{Key: "skill_id", Value: 1},
					{Key: "status", Value: 1},
					{Key: "type", Value: 1},
				},
				Options: options.Index().SetName("questions_skill_status_type"),
			},
		},
		{
			collection: collectionAttempts,
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: -1}},
				Options: options.Index().SetName("attempts_user_created"),
			},
		},
		{
			collection: collectionAttempts,
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "journey_id", Value: 1}, {Key: "number", Value: -1}},
				Options: options.Index().SetName("attempts_journey_number"),
			},
		},
		{
			collection: collectionAppointments,
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "stage", Value: 1}},
				Options: options.Index().SetName("appointments_user_stage"),
			},
		},
		{
			collection: collectionAppointments,
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "assessor_id", Value: 1}, {Key: "scheduled_for", Value: 1}},
				Options: options.Index().SetName("appointments_assessor_schedule"),
			},
		},
		{
			collection: collectionSlots,
			model: mongo.IndexModel{
				Keys: bson.D{
					{Key: "stage", Value: 1},
					{Key: "is_booked", Value: 1},
					{Key: "starts_at", Value: 1},
				},
				Options: options.Index().SetName("slots_open_by_stage"),
			},
		},
		{
			collection: collectionSlots,
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "centre_id", Value: 1}, {Key: "starts_at", Value: 1}},
				Options: options.Index().SetName("slots_centre_schedule"),
			},
		},
		{
			collection: collectionCentres,
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "location_id", Value: 1}, {Key: "is_active", Value: 1}},
				Options: options.Index().SetName("centres_location_active"),
			},
		},
		{
			collection: collectionPracticalTask,
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "skill_id", Value: 1}, {Key: "is_active", Value: 1}},
				Options: options.Index().SetName("practical_tasks_skill_active"),
			},
		},
		{
			// The webhook looks a payment up by reference, and the reference
			// must be globally unique for that lookup to be unambiguous.
			collection: collectionExamPayments,
			model: mongo.IndexModel{
				Keys: bson.D{{Key: "payment_reference", Value: 1}},
				Options: options.Index().
					SetName("exam_payments_reference_unique").
					SetUnique(true),
			},
		},
		{
			// One assessor skill grant per assessor per skill.
			collection: collectionAssessorSkills,
			model: mongo.IndexModel{
				Keys: bson.D{{Key: "assessor_id", Value: 1}, {Key: "skill_id", Value: 1}},
				Options: options.Index().
					SetName("assessor_skills_assessor_skill_unique").
					SetUnique(true),
			},
		},
		{
			collection: collectionAssessorSkills,
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "skill_id", Value: 1}},
				Options: options.Index().SetName("assessor_skills_by_skill"),
			},
		},
		{
			collection: collectionSessions,
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "appointment_id", Value: 1}},
				Options: options.Index().SetName("sessions_appointment"),
			},
		},
		{
			collection: collectionSessions,
			model: mongo.IndexModel{
				Keys:    bson.D{{Key: "assessor_id", Value: 1}, {Key: "created_at", Value: -1}},
				Options: options.Index().SetName("sessions_assessor_created"),
			},
		},
		{
			// One certificate per journey. Unique to prevent race condition
			// in IssueCertificate check-then-insert.
			collection: collectionCertificates,
			model: mongo.IndexModel{
				Keys: bson.D{{Key: "journey_id", Value: 1}},
				Options: options.Index().
					SetName("certificates_journey_unique").
					SetUnique(true),
			},
		},
		{
			// Verification code must be globally unique for public lookups.
			collection: collectionCertificates,
			model: mongo.IndexModel{
				Keys: bson.D{{Key: "verification_code", Value: 1}},
				Options: options.Index().
					SetName("certificates_verification_code_unique").
					SetUnique(true),
			},
		},
	}

	for _, spec := range specs {
		collection := configs.MI.DB.Collection(spec.collection)
		if _, err := collection.Indexes().CreateOne(ctx, spec.model); err != nil {
			var cmdErr mongo.CommandError
			if errors.As(err, &cmdErr) && cmdErr.Code == mongoIndexAlreadyExistsCode {
				continue
			}
			return fmt.Errorf("ensure %s indexes: %w", spec.collection, err)
		}
	}
	return nil
}

// --- Journeys ---

func CreateJourney(ctx context.Context, journey *Journey) error {
	now := time.Now().UTC()
	journey.ID = uuid.New().String()
	journey.CreatedAt = now
	journey.UpdatedAt = now

	_, err := configs.MI.DB.Collection(collectionJourneys).InsertOne(ctx, journey)
	if mongo.IsDuplicateKeyError(err) {
		return ErrJourneyExists
	}
	if err != nil {
		return fmt.Errorf("creating assessment journey: %w", err)
	}
	return nil
}

func GetJourneyForUser(ctx context.Context, userID string) (*Journey, error) {
	var journey Journey
	err := configs.MI.DB.Collection(collectionJourneys).
		FindOne(ctx, bson.M{"user_id": userID}).
		Decode(&journey)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting assessment journey: %w", err)
	}
	return &journey, nil
}

// GetJourneyByIDForUser scopes by user_id in the filter rather than checking
// ownership after the read. The deleted assessments package leaked across
// users precisely because it did the latter.
func GetJourneyByIDForUser(ctx context.Context, id, userID string) (*Journey, error) {
	var journey Journey
	err := configs.MI.DB.Collection(collectionJourneys).
		FindOne(ctx, bson.M{"_id": id, "user_id": userID}).
		Decode(&journey)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting assessment journey: %w", err)
	}
	return &journey, nil
}

func GetJourneyByID(ctx context.Context, id string) (*Journey, error) {
	var journey Journey
	err := configs.MI.DB.Collection(collectionJourneys).
		FindOne(ctx, bson.M{"_id": id}).
		Decode(&journey)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting assessment journey: %w", err)
	}
	return &journey, nil
}

// SaveJourneyStages persists a mutated stage map and current stage together, so
// a transition can never half-apply.
func SaveJourneyStages(ctx context.Context, journey *Journey) error {
	now := time.Now().UTC()
	journey.UpdatedAt = now
	_, err := configs.MI.DB.Collection(collectionJourneys).UpdateOne(
		ctx,
		bson.M{"_id": journey.ID},
		bson.M{"$set": bson.M{
			"current_stage": journey.CurrentStage,
			"stages":        journey.Stages,
			"updated_at":    now,
		}},
	)
	if err != nil {
		return fmt.Errorf("saving assessment journey: %w", err)
	}
	return nil
}

// ListJourneysByStage backs the admin review queue.
func ListJourneysByStage(ctx context.Context, stage Stage, limit int64) ([]Journey, error) {
	findOptions := options.Find().
		SetSort(bson.D{{Key: "updated_at", Value: 1}}).
		SetLimit(limit)
	cursor, err := configs.MI.DB.Collection(collectionJourneys).
		Find(ctx, bson.M{"current_stage": stage}, findOptions)
	if err != nil {
		return nil, fmt.Errorf("listing journeys by stage: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	journeys := []Journey{}
	if err := cursor.All(ctx, &journeys); err != nil {
		return nil, fmt.Errorf("decoding journeys: %w", err)
	}
	return journeys, nil
}

// --- Questions ---

func CreateQuestion(ctx context.Context, question *Question) error {
	now := time.Now().UTC()
	question.ID = uuid.New().String()
	question.CreatedAt = now
	question.UpdatedAt = now
	if question.Status == "" {
		question.Status = QuestionDraft
	}
	if _, err := configs.MI.DB.Collection(collectionQuestions).InsertOne(ctx, question); err != nil {
		return fmt.Errorf("creating assessment question: %w", err)
	}
	return nil
}

func SetQuestionStatus(ctx context.Context, id, approvedBy string, status QuestionStatus) error {
	result, err := configs.MI.DB.Collection(collectionQuestions).UpdateOne(
		ctx,
		bson.M{"_id": id},
		bson.M{"$set": bson.M{
			"status":      status,
			"approved_by": approvedBy,
			"updated_at":  time.Now().UTC(),
		}},
	)
	if err != nil {
		return fmt.Errorf("updating question status: %w", err)
	}
	if result.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// ListQuestionsBySkill returns bank questions, optionally filtered by status.
// Admin/assessor facing: the caller is trusted to see the answer key, but note
// the Question JSON tags still redact it.
func ListQuestionsBySkill(ctx context.Context, skillID string, status QuestionStatus) ([]Question, error) {
	filter := bson.M{"skill_id": skillID}
	if status != "" {
		filter["status"] = status
	}
	cursor, err := configs.MI.DB.Collection(collectionQuestions).
		Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, fmt.Errorf("listing questions: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	questions := []Question{}
	if err := cursor.All(ctx, &questions); err != nil {
		return nil, fmt.Errorf("decoding questions: %w", err)
	}
	return questions, nil
}

// ListQuestionsAdmin returns questions with flexible filtering for admin use.
// Unlike ListQuestionsBySkill, this allows empty skill_id to list all questions.
// ListQuestionsAdmin returns a page of bank questions with the total match
// count. It is paginated rather than capped: a fixed limit silently truncated the
// bank once it grew past the cap, with no way for the caller to reach the rest.
func ListQuestionsAdmin(
	ctx context.Context,
	skillID string,
	status QuestionStatus,
	page, limit int64,
) ([]Question, int64, error) {
	filter := bson.M{}
	if skillID != "" {
		filter["skill_id"] = skillID
	}
	if status != "" {
		filter["status"] = status
	}

	total, err := configs.MI.DB.Collection(collectionQuestions).CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("counting questions (admin): %w", err)
	}

	cursor, err := configs.MI.DB.Collection(collectionQuestions).Find(
		ctx,
		filter,
		options.Find().
			SetSort(bson.D{{Key: "created_at", Value: -1}}).
			SetSkip((page-1)*limit).
			SetLimit(limit),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("listing questions (admin): %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	questions := []Question{}
	if err := cursor.All(ctx, &questions); err != nil {
		return nil, 0, fmt.Errorf("decoding questions (admin): %w", err)
	}
	return questions, total, nil
}

// GetQuestionByID retrieves a single question by ID.
func GetQuestionByID(ctx context.Context, id string) (*Question, error) {
	var question Question
	err := configs.MI.DB.Collection(collectionQuestions).
		FindOne(ctx, bson.M{"_id": id}).
		Decode(&question)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting question: %w", err)
	}
	return &question, nil
}

// DrawApprovedQuestions samples up to `size` approved questions for a skill
// using Mongo's $sample, so each attempt sees a different subset of the bank.
// Only approved questions are eligible: AI drafts must be human-approved first.
func DrawApprovedQuestions(ctx context.Context, skillID string, size int) ([]Question, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"skill_id": skillID,
			"status":   QuestionApproved,
		}}},
		{{Key: "$sample", Value: bson.M{"size": size}}},
	}
	cursor, err := configs.MI.DB.Collection(collectionQuestions).Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("drawing questions: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	questions := []Question{}
	if err := cursor.All(ctx, &questions); err != nil {
		return nil, fmt.Errorf("decoding drawn questions: %w", err)
	}
	return questions, nil
}

// --- Attempts ---

func CreateAttempt(ctx context.Context, attempt *Attempt) error {
	now := time.Now().UTC()
	attempt.ID = uuid.New().String()
	attempt.CreatedAt = now
	attempt.UpdatedAt = now
	if attempt.Answers == nil {
		attempt.Answers = map[string]AttemptAnswer{}
	}
	if _, err := configs.MI.DB.Collection(collectionAttempts).InsertOne(ctx, attempt); err != nil {
		return fmt.Errorf("creating assessment attempt: %w", err)
	}
	return nil
}

func GetAttemptForUser(ctx context.Context, id, userID string) (*Attempt, error) {
	var attempt Attempt
	err := configs.MI.DB.Collection(collectionAttempts).
		FindOne(ctx, bson.M{"_id": id, "user_id": userID}).
		Decode(&attempt)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting assessment attempt: %w", err)
	}
	return &attempt, nil
}

// GetOpenAttempt returns the artisan's in-progress or paused attempt, if any.
// Used to resume rather than start a second parallel attempt.
func GetOpenAttempt(ctx context.Context, journeyID, userID string) (*Attempt, error) {
	var attempt Attempt
	err := configs.MI.DB.Collection(collectionAttempts).
		FindOne(ctx, bson.M{
			"journey_id": journeyID,
			"user_id":    userID,
			"status":     bson.M{"$in": bson.A{AttemptInProgress, AttemptPaused}},
		}).
		Decode(&attempt)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting open attempt: %w", err)
	}
	return &attempt, nil
}

func CountAttempts(ctx context.Context, journeyID string) (int64, error) {
	count, err := configs.MI.DB.Collection(collectionAttempts).
		CountDocuments(ctx, bson.M{"journey_id": journeyID})
	if err != nil {
		return 0, fmt.Errorf("counting attempts: %w", err)
	}
	return count, nil
}

// SaveAnswer upserts a single answer. Scoped to an open attempt owned by the
// user so a submitted attempt cannot be edited after the fact.
func SaveAnswer(ctx context.Context, attemptID, userID string, answer AttemptAnswer) error {
	answer.AnsweredAt = time.Now().UTC()
	result, err := configs.MI.DB.Collection(collectionAttempts).UpdateOne(
		ctx,
		bson.M{
			"_id":     attemptID,
			"user_id": userID,
			"status":  bson.M{"$in": bson.A{AttemptInProgress, AttemptPaused}},
		},
		bson.M{"$set": bson.M{
			"answers." + answer.QuestionID: answer,
			"updated_at":                   answer.AnsweredAt,
		}},
	)
	if err != nil {
		return fmt.Errorf("saving answer: %w", err)
	}
	if result.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAttemptPaused flips an open attempt between paused and in-progress,
// incrementing pause_count when pausing. The count is telemetry only.
func SetAttemptPaused(ctx context.Context, attemptID, userID string, paused bool) error {
	now := time.Now().UTC()
	set := bson.M{"updated_at": now}
	update := bson.M{}
	if paused {
		set["status"] = AttemptPaused
		set["paused_at"] = now
		update["$inc"] = bson.M{"pause_count": 1}
	} else {
		set["status"] = AttemptInProgress
		set["resumed_at"] = now
	}
	update["$set"] = set

	// The from-status guard makes pause/resume idempotent: resuming a running
	// attempt or pausing a paused one matches nothing and reports ErrNotFound
	// rather than corrupting the counter.
	fromStatus := AttemptInProgress
	if !paused {
		fromStatus = AttemptPaused
	}
	result, err := configs.MI.DB.Collection(collectionAttempts).UpdateOne(
		ctx,
		bson.M{"_id": attemptID, "user_id": userID, "status": fromStatus},
		update,
	)
	if err != nil {
		return fmt.Errorf("updating attempt pause state: %w", err)
	}
	if result.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// FinaliseAttempt writes the scored result. Guarded on the attempt still being
// open so a replayed submit cannot rescore.
func FinaliseAttempt(ctx context.Context, attempt *Attempt) error {
	now := time.Now().UTC()
	result, err := configs.MI.DB.Collection(collectionAttempts).UpdateOne(
		ctx,
		bson.M{
			"_id":     attempt.ID,
			"user_id": attempt.UserID,
			"status":  bson.M{"$in": bson.A{AttemptInProgress, AttemptPaused}},
		},
		bson.M{"$set": bson.M{
			"status":          attempt.Status,
			"score":           attempt.Score,
			"max_score":       attempt.MaxScore,
			"passed":          attempt.Passed,
			"feedback":        attempt.Feedback,
			"assessor_report": attempt.AssessorReport,
			"scored_by":       attempt.ScoredBy,
			"audit_prompt":    attempt.AuditPrompt,
			"audit_response":  attempt.AuditResponse,
			"submitted_at":    now,
			"updated_at":      now,
		}},
	)
	if err != nil {
		return fmt.Errorf("finalising attempt: %w", err)
	}
	if result.MatchedCount == 0 {
		return ErrNotFound
	}
	attempt.SubmittedAt = &now
	return nil
}

// ListAbandonedAttempts finds attempts left open past a cutoff, for the SMS
// nudge job. Abandonment, not pausing, is the signal worth acting on.
func ListAbandonedAttempts(ctx context.Context, before time.Time, limit int64) ([]Attempt, error) {
	cursor, err := configs.MI.DB.Collection(collectionAttempts).Find(
		ctx,
		bson.M{
			"status":     bson.M{"$in": bson.A{AttemptInProgress, AttemptPaused}},
			"updated_at": bson.M{"$lte": before},
		},
		options.Find().SetLimit(limit),
	)
	if err != nil {
		return nil, fmt.Errorf("listing abandoned attempts: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	attempts := []Attempt{}
	if err := cursor.All(ctx, &attempts); err != nil {
		return nil, fmt.Errorf("decoding abandoned attempts: %w", err)
	}
	return attempts, nil
}

// --- Practical tasks and centres ---

func ListPracticalTasks(ctx context.Context, skillID string) ([]PracticalTask, error) {
	cursor, err := configs.MI.DB.Collection(collectionPracticalTask).Find(
		ctx,
		bson.M{"skill_id": skillID, "is_active": true},
		options.Find().SetSort(bson.D{{Key: "name", Value: 1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("listing practical tasks: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	tasks := []PracticalTask{}
	if err := cursor.All(ctx, &tasks); err != nil {
		return nil, fmt.Errorf("decoding practical tasks: %w", err)
	}
	return tasks, nil
}

func GetPracticalTask(ctx context.Context, id string) (*PracticalTask, error) {
	var task PracticalTask
	err := configs.MI.DB.Collection(collectionPracticalTask).
		FindOne(ctx, bson.M{"_id": id, "is_active": true}).
		Decode(&task)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting practical task: %w", err)
	}
	return &task, nil
}

// ListCentres returns active centres, narrowed to the artisan's locations when
// any are supplied. An empty locationIDs slice returns all active centres
// rather than none, so an artisan with no saved locations is not stranded.
func ListCentres(ctx context.Context, locationIDs []string) ([]Centre, error) {
	filter := bson.M{"is_active": true}
	if len(locationIDs) > 0 {
		filter["location_id"] = bson.M{"$in": locationIDs}
	}
	cursor, err := configs.MI.DB.Collection(collectionCentres).
		Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("listing centres: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	centres := []Centre{}
	if err := cursor.All(ctx, &centres); err != nil {
		return nil, fmt.Errorf("decoding centres: %w", err)
	}
	return centres, nil
}

// ListAllCentres returns all centres (active and inactive) for admin use.
func ListAllCentres(ctx context.Context) ([]Centre, error) {
	cursor, err := configs.MI.DB.Collection(collectionCentres).
		Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("listing all centres: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	centres := []Centre{}
	if err := cursor.All(ctx, &centres); err != nil {
		return nil, fmt.Errorf("decoding all centres: %w", err)
	}
	return centres, nil
}

// CreateCentre inserts a new centre document.
func CreateCentre(ctx context.Context, centre *Centre) error {
	now := time.Now().UTC()
	centre.ID = uuid.New().String()
	centre.CreatedAt = now
	centre.UpdatedAt = now
	if _, err := configs.MI.DB.Collection(collectionCentres).InsertOne(ctx, centre); err != nil {
		return fmt.Errorf("creating centre: %w", err)
	}
	return nil
}

// CreatePracticalTask inserts a new practical task document.
func CreatePracticalTask(ctx context.Context, task *PracticalTask) error {
	now := time.Now().UTC()
	task.ID = uuid.New().String()
	task.CreatedAt = now
	task.UpdatedAt = now
	if _, err := configs.MI.DB.Collection(collectionPracticalTask).InsertOne(ctx, task); err != nil {
		return fmt.Errorf("creating practical task: %w", err)
	}
	return nil
}

// ListAllPracticalTasks returns all tasks for a skill (active and inactive) for admin use.
func ListAllPracticalTasks(ctx context.Context, skillID string) ([]PracticalTask, error) {
	filter := bson.M{}
	if skillID != "" {
		filter["skill_id"] = skillID
	}
	cursor, err := configs.MI.DB.Collection(collectionPracticalTask).Find(
		ctx,
		filter,
		options.Find().SetSort(bson.D{{Key: "name", Value: 1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("listing all practical tasks: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	tasks := []PracticalTask{}
	if err := cursor.All(ctx, &tasks); err != nil {
		return nil, fmt.Errorf("decoding all practical tasks: %w", err)
	}
	return tasks, nil
}

// ListAllJourneys returns journeys filtered by stage and/or status with
// pagination, for admin use.
//
// status always means "the status of the journey's current stage". With a stage
// given, the path is known and the filter is direct. Without one it has to match
// whichever stage each journey currently sits on, which is expressed as an $or
// across the stages; previously status was silently dropped in that case, so
// filtering by status alone returned every journey.
func ListAllJourneys(ctx context.Context, stage Stage, status StageStatus, page, limit int64) ([]Journey, int64, error) {
	filter := bson.M{}
	if stage != "" {
		filter["current_stage"] = stage
	}
	if status != "" {
		if stage != "" {
			filter["stages."+string(stage)+".status"] = status
		} else {
			// One clause per stage, each pinning current_stage to that stage so the
			// status is only ever read from the stage the journey is actually on.
			stages := StageOrder()
			clauses := make([]bson.M, 0, len(stages))
			for _, s := range stages {
				clauses = append(clauses, bson.M{
					"current_stage":                   s,
					"stages." + string(s) + ".status": status,
				})
			}
			filter["$or"] = clauses
		}
	}

	total, err := configs.MI.DB.Collection(collectionJourneys).CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("counting journeys: %w", err)
	}

	skip := (page - 1) * limit
	findOptions := options.Find().
		SetSort(bson.D{{Key: "updated_at", Value: -1}}).
		SetSkip(skip).
		SetLimit(limit)

	cursor, err := configs.MI.DB.Collection(collectionJourneys).Find(ctx, filter, findOptions)
	if err != nil {
		return nil, 0, fmt.Errorf("listing all journeys: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	journeys := []Journey{}
	if err := cursor.All(ctx, &journeys); err != nil {
		return nil, 0, fmt.Errorf("decoding all journeys: %w", err)
	}
	return journeys, total, nil
}

// ListCertificatesByUser returns certificates for a given user.
func ListCertificatesByUser(ctx context.Context, userID string) ([]Certificate, error) {
	filter := bson.M{}
	if userID != "" {
		filter["user_id"] = userID
	}
	cursor, err := configs.MI.DB.Collection(collectionCertificates).Find(
		ctx,
		filter,
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(100),
	)
	if err != nil {
		return nil, fmt.Errorf("listing certificates: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	certs := []Certificate{}
	if err := cursor.All(ctx, &certs); err != nil {
		return nil, fmt.Errorf("decoding certificates: %w", err)
	}
	return certs, nil
}

// --- Slots ---

func ListOpenSlots(ctx context.Context, stage Stage, centreID string, from time.Time) ([]Slot, error) {
	filter := bson.M{
		"stage":     stage,
		"is_booked": false,
		"starts_at": bson.M{"$gte": from},
	}
	if centreID != "" {
		filter["centre_id"] = centreID
	}
	cursor, err := configs.MI.DB.Collection(collectionSlots).
		Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "starts_at", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("listing open slots: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	slots := []Slot{}
	if err := cursor.All(ctx, &slots); err != nil {
		return nil, fmt.Errorf("decoding slots: %w", err)
	}
	return slots, nil
}

func CreateSlot(ctx context.Context, slot *Slot) error {
	now := time.Now().UTC()
	slot.ID = uuid.New().String()
	slot.CreatedAt = now
	slot.UpdatedAt = now
	if _, err := configs.MI.DB.Collection(collectionSlots).InsertOne(ctx, slot); err != nil {
		return fmt.Errorf("creating slot: %w", err)
	}
	return nil
}

// ClaimSlot books a slot atomically. The is_booked: false guard in the filter
// is what prevents two artisans winning the same slot in a race; a losing
// caller gets ErrSlotUnavailable rather than a silent double-booking.
func ClaimSlot(ctx context.Context, slotID, userID string) (*Slot, error) {
	now := time.Now().UTC()
	var slot Slot
	err := configs.MI.DB.Collection(collectionSlots).FindOneAndUpdate(
		ctx,
		bson.M{"_id": slotID, "is_booked": false},
		bson.M{"$set": bson.M{
			"is_booked":  true,
			"booked_by":  userID,
			"updated_at": now,
		}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&slot)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrSlotUnavailable
	}
	if err != nil {
		return nil, fmt.Errorf("claiming slot: %w", err)
	}
	return &slot, nil
}

// ReleaseSlot returns a slot to the pool, used when booking fails after the
// claim (for example the payment checkout could not be created).
func ReleaseSlot(ctx context.Context, slotID string) error {
	_, err := configs.MI.DB.Collection(collectionSlots).UpdateOne(
		ctx,
		bson.M{"_id": slotID},
		bson.M{
			"$set":   bson.M{"is_booked": false, "updated_at": time.Now().UTC()},
			"$unset": bson.M{"booked_by": ""},
		},
	)
	if err != nil {
		return fmt.Errorf("releasing slot: %w", err)
	}
	return nil
}

// --- Appointments ---

func CreateAppointment(ctx context.Context, appointment *Appointment) error {
	now := time.Now().UTC()
	appointment.ID = uuid.New().String()
	appointment.CreatedAt = now
	appointment.UpdatedAt = now
	if _, err := configs.MI.DB.Collection(collectionAppointments).InsertOne(ctx, appointment); err != nil {
		return fmt.Errorf("creating appointment: %w", err)
	}
	return nil
}

func GetAppointmentByID(ctx context.Context, id string) (*Appointment, error) {
	var appointment Appointment
	err := configs.MI.DB.Collection(collectionAppointments).
		FindOne(ctx, bson.M{"_id": id}).
		Decode(&appointment)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting appointment: %w", err)
	}
	return &appointment, nil
}

// GetActiveAppointmentForUserStage returns the artisan's current appointment for
// a stage: the one that has not yet been decided. It is scoped by user_id so an
// artisan can only ever read their own.
//
// Ordering by scheduled_for descending returns the latest booking when a stage
// has been rebooked after a cancellation, since cancelled appointments are
// deleted rather than kept.
func GetActiveAppointmentForUserStage(ctx context.Context, userID string, stage Stage) (*Appointment, error) {
	var appointment Appointment
	err := configs.MI.DB.Collection(collectionAppointments).FindOne(
		ctx,
		bson.M{
			"user_id":    userID,
			"stage":      stage,
			"decided_at": bson.M{"$exists": false},
		},
		options.FindOne().SetSort(bson.D{{Key: "scheduled_for", Value: -1}}),
	).Decode(&appointment)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting active appointment for stage: %w", err)
	}
	return &appointment, nil
}

// GetAppointmentForUser scopes by user_id so an artisan cannot act on another
// artisan's appointment by guessing its id.
func GetAppointmentForUser(ctx context.Context, id, userID string) (*Appointment, error) {
	var appointment Appointment
	err := configs.MI.DB.Collection(collectionAppointments).
		FindOne(ctx, bson.M{"_id": id, "user_id": userID}).
		Decode(&appointment)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting appointment for user: %w", err)
	}
	return &appointment, nil
}

// DeleteAppointment removes an appointment outright. Used when an artisan
// cancels to reschedule: the slot is released and the booking leaves no trace,
// so the stage returns cleanly to "available" and can be rebooked.
func DeleteAppointment(ctx context.Context, id string) error {
	if _, err := configs.MI.DB.Collection(collectionAppointments).
		DeleteOne(ctx, bson.M{"_id": id}); err != nil {
		return fmt.Errorf("deleting appointment: %w", err)
	}
	return nil
}

// GetCentre looks up a single centre regardless of active state, so a booking
// made at a centre that was later deactivated still renders its venue.
func GetCentre(ctx context.Context, id string) (*Centre, error) {
	var centre Centre
	err := configs.MI.DB.Collection(collectionCentres).
		FindOne(ctx, bson.M{"_id": id}).
		Decode(&centre)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting centre: %w", err)
	}
	return &centre, nil
}

// GetAppointmentForAssessor scopes by assessor_id so one assessor cannot read
// or grade another's appointments.
func GetAppointmentForAssessor(ctx context.Context, id, assessorID string) (*Appointment, error) {
	var appointment Appointment
	err := configs.MI.DB.Collection(collectionAppointments).
		FindOne(ctx, bson.M{"_id": id, "assessor_id": assessorID}).
		Decode(&appointment)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting appointment: %w", err)
	}
	return &appointment, nil
}

func ListAppointmentsForAssessor(ctx context.Context, assessorID string, from time.Time) ([]Appointment, error) {
	cursor, err := configs.MI.DB.Collection(collectionAppointments).Find(
		ctx,
		bson.M{"assessor_id": assessorID, "scheduled_for": bson.M{"$gte": from}},
		options.Find().SetSort(bson.D{{Key: "scheduled_for", Value: 1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("listing assessor appointments: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	appointments := []Appointment{}
	if err := cursor.All(ctx, &appointments); err != nil {
		return nil, fmt.Errorf("decoding appointments: %w", err)
	}
	return appointments, nil
}

func ListAppointmentsForUser(ctx context.Context, userID string) ([]Appointment, error) {
	cursor, err := configs.MI.DB.Collection(collectionAppointments).Find(
		ctx,
		bson.M{"user_id": userID},
		options.Find().SetSort(bson.D{{Key: "scheduled_for", Value: -1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("listing user appointments: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	appointments := []Appointment{}
	if err := cursor.All(ctx, &appointments); err != nil {
		return nil, fmt.Errorf("decoding appointments: %w", err)
	}
	return appointments, nil
}

func UpdateAppointment(ctx context.Context, id string, updates bson.M) error {
	updates["updated_at"] = time.Now().UTC()
	result, err := configs.MI.DB.Collection(collectionAppointments).UpdateOne(
		ctx,
		bson.M{"_id": id},
		bson.M{"$set": updates},
	)
	if err != nil {
		return fmt.Errorf("updating appointment: %w", err)
	}
	if result.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Exam payments ---

func CreateExamPayment(ctx context.Context, payment *ExamPayment) error {
	now := time.Now().UTC()
	payment.ID = uuid.New().String()
	payment.CreatedAt = now
	payment.UpdatedAt = now
	if payment.Status == "" {
		payment.Status = PaymentPending
	}
	if _, err := configs.MI.DB.Collection(collectionExamPayments).InsertOne(ctx, payment); err != nil {
		return fmt.Errorf("creating exam payment: %w", err)
	}
	return nil
}

func GetExamPaymentByReference(ctx context.Context, reference string) (*ExamPayment, error) {
	var payment ExamPayment
	err := configs.MI.DB.Collection(collectionExamPayments).
		FindOne(ctx, bson.M{"payment_reference": reference}).
		Decode(&payment)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting exam payment: %w", err)
	}
	return &payment, nil
}

// MarkExamPaymentPaid transitions a pending payment to paid. The
// status: pending guard makes it idempotent: a replayed Cellulant webhook
// matches nothing the second time, so the exam is never double-confirmed.
// The bool reports whether this call was the one that made the transition.
func MarkExamPaymentPaid(ctx context.Context, id string) (bool, error) {
	result, err := configs.MI.DB.Collection(collectionExamPayments).UpdateOne(
		ctx,
		bson.M{"_id": id, "status": PaymentPending},
		bson.M{"$set": bson.M{
			"status":     PaymentPaid,
			"updated_at": time.Now().UTC(),
		}},
	)
	if err != nil {
		return false, fmt.Errorf("marking exam payment paid: %w", err)
	}
	return result.MatchedCount > 0, nil
}

func UpdateExamPaymentStatus(ctx context.Context, id, status string) error {
	_, err := configs.MI.DB.Collection(collectionExamPayments).UpdateOne(
		ctx,
		bson.M{"_id": id},
		bson.M{"$set": bson.M{"status": status, "updated_at": time.Now().UTC()}},
	)
	if err != nil {
		return fmt.Errorf("updating exam payment status: %w", err)
	}
	return nil
}

// --- Assessor Skills ---

func CreateAssessorSkill(ctx context.Context, skill *AssessorSkill) error {
	now := time.Now().UTC()
	skill.ID = uuid.New().String()
	skill.CreatedAt = now
	skill.UpdatedAt = now
	_, err := configs.MI.DB.Collection(collectionAssessorSkills).InsertOne(ctx, skill)
	if mongo.IsDuplicateKeyError(err) {
		return ErrAssessorSkillExists
	}
	if err != nil {
		return fmt.Errorf("creating assessor skill: %w", err)
	}
	return nil
}

func ListAssessorSkillsByAssessor(ctx context.Context, assessorID string) ([]AssessorSkill, error) {
	cursor, err := configs.MI.DB.Collection(collectionAssessorSkills).Find(
		ctx,
		bson.M{"assessor_id": assessorID},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("listing assessor skills: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	skills := []AssessorSkill{}
	if err := cursor.All(ctx, &skills); err != nil {
		return nil, fmt.Errorf("decoding assessor skills: %w", err)
	}
	return skills, nil
}

func ListAssessorSkillsBySkill(ctx context.Context, skillID string) ([]AssessorSkill, error) {
	cursor, err := configs.MI.DB.Collection(collectionAssessorSkills).Find(
		ctx,
		bson.M{"skill_id": skillID},
	)
	if err != nil {
		return nil, fmt.Errorf("listing assessor skills by skill: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	skills := []AssessorSkill{}
	if err := cursor.All(ctx, &skills); err != nil {
		return nil, fmt.Errorf("decoding assessor skills: %w", err)
	}
	return skills, nil
}

func ListAllAssessorSkills(ctx context.Context) ([]AssessorSkill, error) {
	cursor, err := configs.MI.DB.Collection(collectionAssessorSkills).Find(
		ctx,
		bson.M{},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("listing all assessor skills: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	skills := []AssessorSkill{}
	if err := cursor.All(ctx, &skills); err != nil {
		return nil, fmt.Errorf("decoding assessor skills: %w", err)
	}
	return skills, nil
}

func DeleteAssessorSkill(ctx context.Context, id string) error {
	result, err := configs.MI.DB.Collection(collectionAssessorSkills).DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return fmt.Errorf("deleting assessor skill: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Sessions ---

func CreateSession(ctx context.Context, session *Session) error {
	now := time.Now().UTC()
	session.ID = uuid.New().String()
	session.CreatedAt = now
	session.UpdatedAt = now
	if _, err := configs.MI.DB.Collection(collectionSessions).InsertOne(ctx, session); err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	return nil
}

func GetSessionByAppointment(ctx context.Context, appointmentID string) (*Session, error) {
	var session Session
	err := configs.MI.DB.Collection(collectionSessions).
		FindOne(ctx, bson.M{"appointment_id": appointmentID}).
		Decode(&session)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	return &session, nil
}

func ListSessionsForAssessor(ctx context.Context, assessorID string) ([]Session, error) {
	cursor, err := configs.MI.DB.Collection(collectionSessions).Find(
		ctx,
		bson.M{"assessor_id": assessorID},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	sessions := []Session{}
	if err := cursor.All(ctx, &sessions); err != nil {
		return nil, fmt.Errorf("decoding sessions: %w", err)
	}
	return sessions, nil
}

// --- Slots (additional) ---

func GetSlotByID(ctx context.Context, id string) (*Slot, error) {
	var slot Slot
	err := configs.MI.DB.Collection(collectionSlots).
		FindOne(ctx, bson.M{"_id": id}).
		Decode(&slot)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting slot: %w", err)
	}
	return &slot, nil
}

func ListSlotsForAssessor(ctx context.Context, assessorID string) ([]Slot, error) {
	cursor, err := configs.MI.DB.Collection(collectionSlots).Find(
		ctx,
		bson.M{"assessor_id": assessorID},
		options.Find().SetSort(bson.D{{Key: "starts_at", Value: 1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("listing assessor slots: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	slots := []Slot{}
	if err := cursor.All(ctx, &slots); err != nil {
		return nil, fmt.Errorf("decoding slots: %w", err)
	}
	return slots, nil
}

func DeleteSlot(ctx context.Context, id, assessorID string) error {
	result, err := configs.MI.DB.Collection(collectionSlots).DeleteOne(
		ctx,
		bson.M{"_id": id, "assessor_id": assessorID, "is_booked": false},
	)
	if err != nil {
		return fmt.Errorf("deleting slot: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// CountUpcomingAppointments returns how many future appointments an assessor has.
// Used by FindQualifiedAssessor to prefer the least-busy assessor.
func CountUpcomingAppointments(ctx context.Context, assessorID string, from time.Time) (int64, error) {
	count, err := configs.MI.DB.Collection(collectionAppointments).CountDocuments(
		ctx,
		bson.M{"assessor_id": assessorID, "scheduled_for": bson.M{"$gte": from}},
	)
	if err != nil {
		return 0, fmt.Errorf("counting upcoming appointments: %w", err)
	}
	return count, nil
}
