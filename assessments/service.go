package assessments

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"backend/profiles"
	"backend/timeutil"
	"backend/users"
)

// Tunable policy constants. These are the product knobs most likely to change,
// so they are named and grouped rather than inlined at their use sites.
const (
	// WrittenRetakeCooldown is the wait between written attempts. Attempts are
	// unlimited: the cooldown exists to stop bank-harvesting by rapid retry,
	// not to ration access. Currently applied as business hours (skipping
	// weekends) via AddBusinessHours, so a Friday failure reopens Tuesday.
	WrittenRetakeCooldown = 48 * time.Hour

	// WrittenQuestionTarget is how many questions an attempt draws, and
	// WrittenQuestionMinimum is the smallest bank that can run an assessment.
	// Drawing from a bank barely larger than the draw would leak the whole bank
	// over a few attempts, which is why the minimum is enforced.
	WrittenQuestionTarget  = 18
	WrittenQuestionMinimum = 15

	// WrittenPassPercent is the threshold to pass the written assessment.
	// The written stage is a genuine filter that validates the artisan
	// understands their trade to a professional standard.
	WrittenPassPercent = 80

	// PracticalPassPercent applies to the assessor's practical score.
	PracticalPassPercent = 60

	// AbandonedAttemptAfter is how long an untouched open attempt waits before
	// the nudge job SMSes the artisan.
	AbandonedAttemptAfter = 72 * time.Hour
)

var (
	ErrProfileIncomplete   = errors.New("complete your profile before starting assessments")
	ErrProfileNotApproved  = errors.New("your profile must be approved before starting assessments")
	ErrStageNotAvailable   = errors.New("this assessment stage is not available yet")
	ErrCooldownActive      = errors.New("you can retake this assessment later")
	ErrInsufficientBank    = errors.New("not enough approved questions for this skill yet")
	ErrAttemptOpen         = errors.New("you already have an assessment in progress")
	ErrUnknownQuestion     = errors.New("that question is not part of this attempt")
	ErrExamNotPaid         = errors.New("the practical exam fee has not been paid")
	ErrInvalidStage        = errors.New("invalid assessment stage")
	ErrPracticalTaskNeeded = errors.New("select the task you want to be assessed on")
	ErrCentreNeeded        = errors.New("select a verification centre")
)

// StageOrder is the canonical pipeline order. StageCertified is terminal.
func StageOrder() []Stage {
	return []Stage{
		StageProfileReview,
		StageOral,
		StageWritten,
		StagePractical,
		StageCertified,
	}
}

// AddBusinessHours adds the given duration to from, skipping weekends.
//
// Delegates to timeutil so the SLA job and this package cannot disagree about
// what a business hour is; retained as a named function because the stage logic
// and its tests read better with it in the domain vocabulary.
func AddBusinessHours(from time.Time, d time.Duration) time.Time {
	return timeutil.AddBusinessHours(from, d)
}

// NextStage returns the stage following the given one, and false when the
// argument is terminal or unknown.
func NextStage(stage Stage) (Stage, bool) {
	order := StageOrder()
	for i, candidate := range order {
		if candidate == stage && i+1 < len(order) {
			return order[i+1], true
		}
	}
	return "", false
}

// NewJourneyStages builds the initial stage map: profile review is immediately
// available (the caller has already verified profile completeness) and every
// later stage is locked.
func NewJourneyStages(now time.Time) map[Stage]StageState {
	stages := map[Stage]StageState{}
	for _, stage := range StageOrder() {
		status := StatusLocked
		if stage == StageProfileReview {
			status = StatusAvailable
		}
		stages[stage] = StageState{Status: status, UpdatedAt: now}
	}
	return stages
}

// StartJourney creates the pipeline for an artisan whose profile is complete.
// The completeness gate reuses profiles.GetProfileCompletion, which is the same
// function jobrequests already gates on, so "complete" means one thing across
// the product.
func StartJourney(ctx context.Context, userID string) (*Journey, error) {
	profile, err := profiles.GetFullProfile(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("loading profile: %w", err)
	}
	if !profiles.GetProfileCompletion(profile).Complete {
		return nil, ErrProfileIncomplete
	}
	if users.EffectiveProfileStatus(profile.User) != users.ProfileStatusApproved {
		return nil, ErrProfileNotApproved
	}
	// The profile wizard enforces exactly one skill, so index 0 is the trade.
	skillID := ""
	if len(profile.Skills) > 0 {
		skillID = profile.Skills[0]
	}
	if strings.TrimSpace(skillID) == "" {
		return nil, ErrProfileIncomplete
	}

	now := time.Now().UTC()
	journey := &Journey{
		UserID:       userID,
		SkillID:      skillID,
		CurrentStage: StageProfileReview,
		Stages:       NewJourneyStages(now),
	}
	if err := CreateJourney(ctx, journey); err != nil {
		return nil, err
	}
	return journey, nil
}

// ApplyDecision records a pass or fail for a stage and moves the journey.
//
// It is pure: it mutates the journey in memory and the caller persists. That
// keeps the transition rules testable without a database, which matters because
// this is the part most likely to grow subtle bugs.
func ApplyDecision(
	journey *Journey,
	stage Stage,
	passed bool,
	decidedBy, notes, feedback string,
	now time.Time,
) error {
	if journey == nil {
		return ErrNotFound
	}
	state, ok := journey.Stages[stage]
	if !ok {
		return ErrInvalidStage
	}

	state.Attempts++
	state.Notes = notes
	state.Feedback = feedback
	state.DecidedBy = decidedBy
	state.DecidedAt = &now
	state.UpdatedAt = now

	if passed {
		state.Status = StatusPassed
		journey.Stages[stage] = state

		next, hasNext := NextStage(stage)
		if !hasNext {
			journey.CurrentStage = stage
			return nil
		}
		nextState := journey.Stages[next]
		nextState.UpdatedAt = now
		// Certification is an outcome, not something the artisan does, so it
		// is marked passed rather than made available.
		if next == StageCertified {
			nextState.Status = StatusPassed
		} else if nextState.Status == StatusLocked {
			nextState.Status = StatusAvailable
		}
		journey.Stages[next] = nextState
		journey.CurrentStage = next
		return nil
	}

	state.Status = StatusFailed
	// A failure is not terminal. The written stage reopens after a cooldown;
	// the interview and practical stages can be rebooked immediately, since
	// their scarcity is already limited by assessor availability.
	if stage == StageWritten {
		nextAttempt := AddBusinessHours(now, WrittenRetakeCooldown)
		state.NextAttemptAt = &nextAttempt
	} else {
		state.NextAttemptAt = &now
	}
	journey.Stages[stage] = state
	journey.CurrentStage = stage
	return nil
}

// CanAttemptStage reports whether an artisan may start or book a stage now.
// The returned time is when they may next try, and is only meaningful when the
// bool is false because of a cooldown.
func CanAttemptStage(journey *Journey, stage Stage, now time.Time) (bool, *time.Time, error) {
	if journey == nil {
		return false, nil, ErrNotFound
	}
	state, ok := journey.Stages[stage]
	if !ok {
		return false, nil, ErrInvalidStage
	}

	switch state.Status {
	case StatusAvailable, StatusInProgress:
		return true, nil, nil
	case StatusFailed:
		// Reopened by cooldown expiry. NextAttemptAt is always set on failure.
		if state.NextAttemptAt == nil || now.Before(*state.NextAttemptAt) {
			return false, state.NextAttemptAt, ErrCooldownActive
		}
		return true, nil, nil
	default:
		// locked, scheduled, awaiting_payment, awaiting_review, passed
		return false, nil, ErrStageNotAvailable
	}
}

// CarriedNotesFor returns the feedback to surface on a fresh attempt: the
// previous decision's notes and artisan feedback, so a retry starts from what
// went wrong rather than from nothing.
func CarriedNotesFor(journey *Journey, stage Stage) string {
	if journey == nil {
		return ""
	}
	state, ok := journey.Stages[stage]
	if !ok {
		return ""
	}
	parts := []string{}
	if strings.TrimSpace(state.Feedback) != "" {
		parts = append(parts, strings.TrimSpace(state.Feedback))
	}
	if strings.TrimSpace(state.Notes) != "" {
		parts = append(parts, strings.TrimSpace(state.Notes))
	}
	return strings.Join(parts, "\n\n")
}

// Shuffler matches rand.Shuffle's signature so tests can inject a
// deterministic permutation.
type Shuffler func(n int, swap func(i, j int))

// DefaultShuffler is the production shuffle.
func DefaultShuffler(n int, swap func(i, j int)) {
	rand.Shuffle(n, swap)
}

// BuildAttemptQuestions freezes drawn bank questions into an attempt, shuffling
// the option order per question.
//
// The snapshot is the point: a later edit to the bank must not change the grade
// of an already-submitted attempt, and the shuffled order must survive a
// pause/resume so the artisan sees the same paper they left.
func BuildAttemptQuestions(questions []Question, shuffle Shuffler) []AttemptQuestion {
	if shuffle == nil {
		shuffle = DefaultShuffler
	}
	built := make([]AttemptQuestion, 0, len(questions))
	for _, question := range questions {
		options := make([]QuestionOption, len(question.Options))
		copy(options, question.Options)
		shuffle(len(options), func(i, j int) {
			options[i], options[j] = options[j], options[i]
		})

		correct := make([]string, len(question.CorrectOptionIDs))
		copy(correct, question.CorrectOptionIDs)

		built = append(built, AttemptQuestion{
			QuestionID:       question.ID,
			Type:             question.Type,
			Prompt:           question.Prompt,
			Options:          options,
			Points:           question.Points,
			CorrectOptionIDs: correct,
			Rubric:           question.Rubric,
		})
	}
	return built
}

// ObjectiveResult is the outcome of deterministic scoring.
type ObjectiveResult struct {
	// Score is points earned on objective questions only.
	Score int
	// MaxScore is the total across every question in the attempt, including
	// free text, so a percentage stays correct once AI scoring fills the rest in.
	MaxScore int
	// FreeText are the questions still needing AI or human scoring.
	FreeText []AttemptQuestion
}

// ScoreObjective scores every non-free-text question deterministically.
//
// Choice questions require an exact set match for their points. Partial credit
// is intentionally omitted: with a lenient pass mark it would mostly add
// grading ambiguity, and a wrong-but-close answer on a trade safety question is
// not worth half marks.
func ScoreObjective(questions []AttemptQuestion, answers map[string]AttemptAnswer) ObjectiveResult {
	result := ObjectiveResult{FreeText: []AttemptQuestion{}}
	for _, question := range questions {
		result.MaxScore += question.Points

		if question.Type == QuestionFreeText {
			result.FreeText = append(result.FreeText, question)
			continue
		}

		answer, answered := answers[question.QuestionID]
		if !answered {
			continue
		}
		if sameOptionSet(question.CorrectOptionIDs, answer.OptionIDs) {
			result.Score += question.Points
		}
	}
	return result
}

// sameOptionSet compares two option-ID sets ignoring order and duplicates.
func sameOptionSet(expected, actual []string) bool {
	if len(expected) == 0 {
		return false
	}
	normalise := func(values []string) []string {
		seen := map[string]struct{}{}
		out := make([]string, 0, len(values))
		for _, value := range values {
			trimmed := strings.TrimSpace(value)
			if trimmed == "" {
				continue
			}
			if _, duplicate := seen[trimmed]; duplicate {
				continue
			}
			seen[trimmed] = struct{}{}
			out = append(out, trimmed)
		}
		sort.Strings(out)
		return out
	}
	left, right := normalise(expected), normalise(actual)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// Passed reports whether a score clears a percentage threshold. A zero max
// score never passes, so an empty or misconfigured paper cannot certify anyone.
func Passed(score, maxScore, passPercent int) bool {
	if maxScore <= 0 {
		return false
	}
	return score*100 >= passPercent*maxScore
}

// IntegrityFlags summarises the per-answer integrity signals for an assessor.
//
// This returns human-readable flags rather than a verdict on purpose: these
// signals are weak evidence (pasting a long answer and taking a phone call are
// both normal), so a human decides what they mean. Nothing here affects a score.
func IntegrityFlags(attempt *Attempt) []string {
	if attempt == nil {
		return nil
	}
	flags := []string{}
	totalPastes, totalBlurs, instant := 0, 0, 0
	for _, answer := range attempt.Answers {
		totalPastes += answer.PasteCount
		totalBlurs += answer.BlurCount
		// Under two seconds on a question is faster than reading it.
		if answer.DwellMS > 0 && answer.DwellMS < 2000 {
			instant++
		}
	}
	if totalPastes > 0 {
		flags = append(flags, fmt.Sprintf("pasted content %d time(s)", totalPastes))
	}
	if totalBlurs > 3 {
		flags = append(flags, fmt.Sprintf("left the page %d time(s)", totalBlurs))
	}
	if instant > 0 {
		flags = append(flags, fmt.Sprintf("answered %d question(s) in under 2s", instant))
	}
	if attempt.PauseCount > 0 {
		// Reported for context only. Pausing is expected on metered mobile
		// data and unreliable power, and is explicitly not a cheating signal.
		flags = append(flags, fmt.Sprintf("paused %d time(s)", attempt.PauseCount))
	}
	return flags
}

// StartWrittenAttempt opens a written attempt, resuming an existing open one
// instead of creating a duplicate.
func StartWrittenAttempt(ctx context.Context, userID string) (*Attempt, error) {
	journey, err := GetJourneyForUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	// Resuming beats starting: an artisan who closed the tab should come back
	// to the same paper, not a fresh draw.
	if existing, err := GetOpenAttempt(ctx, journey.ID, userID); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	now := time.Now().UTC()
	if _, _, err := CanAttemptStage(journey, StageWritten, now); err != nil {
		return nil, err
	}

	drawn, err := DrawApprovedQuestions(ctx, journey.SkillID, WrittenQuestionTarget)
	if err != nil {
		return nil, err
	}
	if len(drawn) < WrittenQuestionMinimum {
		return nil, ErrInsufficientBank
	}

	count, err := CountAttempts(ctx, journey.ID)
	if err != nil {
		return nil, err
	}

	attempt := &Attempt{
		JourneyID:    journey.ID,
		UserID:       userID,
		SkillID:      journey.SkillID,
		Number:       int(count) + 1,
		Status:       AttemptInProgress,
		Questions:    BuildAttemptQuestions(drawn, DefaultShuffler),
		Answers:      map[string]AttemptAnswer{},
		CarriedNotes: CarriedNotesFor(journey, StageWritten),
	}
	if err := CreateAttempt(ctx, attempt); err != nil {
		return nil, err
	}

	state := journey.Stages[StageWritten]
	state.Status = StatusInProgress
	state.UpdatedAt = now
	journey.Stages[StageWritten] = state
	if err := SaveJourneyStages(ctx, journey); err != nil {
		return nil, err
	}

	return attempt, nil
}

// RecordAnswer autosaves one answer. Rejecting unknown question IDs keeps an
// attempt's answer map aligned with its frozen question snapshot.
func RecordAnswer(ctx context.Context, attemptID, userID string, answer AttemptAnswer) error {
	attempt, err := GetAttemptForUser(ctx, attemptID, userID)
	if err != nil {
		return err
	}
	known := false
	for _, question := range attempt.Questions {
		if question.QuestionID == answer.QuestionID {
			known = true
			break
		}
	}
	if !known {
		return ErrUnknownQuestion
	}
	return SaveAnswer(ctx, attemptID, userID, answer)
}

// Scorer scores free-text answers and writes narrative feedback. Implemented by
// integrations/bedrock.go in production; an interface here so the pipeline is
// testable and so an outage has an obvious fallback path.
type Scorer interface {
	ScoreFreeText(ctx context.Context, requests []FreeTextRequest) (FreeTextResult, error)
}

// FreeTextRequest is one free-text answer to be scored.
type FreeTextRequest struct {
	QuestionID string
	Prompt     string
	Rubric     string
	Answer     string
	MaxPoints  int
}

// FreeTextResult is the model's scoring plus the narrative artefacts.
type FreeTextResult struct {
	Scores map[string]int
	// Feedback is artisan-facing plain language.
	Feedback string
	// AssessorReport is read by the next human assessor.
	AssessorReport string
	// Prompt and Raw are persisted for audit so a disputed score is reviewable.
	Prompt string
	Raw    string
}

// SubmitAttempt scores and closes an attempt, then advances the journey.
//
// Objective questions are always scored locally, so a Bedrock outage degrades
// to "free text needs a human" rather than stranding the artisan mid-pipeline.
func SubmitAttempt(ctx context.Context, attemptID, userID string, scorer Scorer) (*Attempt, error) {
	attempt, err := GetAttemptForUser(ctx, attemptID, userID)
	if err != nil {
		return nil, err
	}
	if attempt.Status != AttemptInProgress && attempt.Status != AttemptPaused {
		return nil, ErrNotFound
	}

	objective := ScoreObjective(attempt.Questions, attempt.Answers)
	attempt.Score = objective.Score
	attempt.MaxScore = objective.MaxScore
	attempt.ScoredBy = "auto"
	attempt.Status = AttemptScored

	if len(objective.FreeText) > 0 {
		requests := make([]FreeTextRequest, 0, len(objective.FreeText))
		for _, question := range objective.FreeText {
			requests = append(requests, FreeTextRequest{
				QuestionID: question.QuestionID,
				Prompt:     question.Prompt,
				Rubric:     question.Rubric,
				Answer:     attempt.Answers[question.QuestionID].Text,
				MaxPoints:  question.Points,
			})
		}

		result, scoreErr := scoreFreeText(ctx, scorer, requests)
		if scoreErr != nil {
			// Degrade, do not fail: keep the objective score, hand the free
			// text to a human, and let the artisan see partial results now.
			attempt.Status = AttemptAwaitingReview
			attempt.ScoredBy = "auto"
			attempt.Feedback = "Your multiple-choice answers are scored. " +
				"An assessor will review your written answers shortly."
		} else {
			for questionID, points := range result.Scores {
				_ = questionID // accumulate points per question
				attempt.Score += points
			}
			attempt.Feedback = result.Feedback
			attempt.AssessorReport = result.AssessorReport
			attempt.AuditPrompt = result.Prompt
			attempt.AuditResponse = result.Raw
			attempt.ScoredBy = "bedrock"
		}
	}

	attempt.Passed = attempt.Status == AttemptScored &&
		Passed(attempt.Score, attempt.MaxScore, WrittenPassPercent)

	if err := FinaliseAttempt(ctx, attempt); err != nil {
		return nil, err
	}

	journey, err := GetJourneyByID(ctx, attempt.JourneyID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if attempt.Status == AttemptAwaitingReview {
		// Hold the stage open for the human rather than deciding it.
		state := journey.Stages[StageWritten]
		state.Status = StatusAwaitingReview
		state.Score = attempt.Score
		state.MaxScore = attempt.MaxScore
		state.UpdatedAt = now
		journey.Stages[StageWritten] = state
	} else {
		if err := ApplyDecision(
			journey,
			StageWritten,
			attempt.Passed,
			"auto",
			"",
			attempt.Feedback,
			now,
		); err != nil {
			return nil, err
		}
		state := journey.Stages[StageWritten]
		state.Score = attempt.Score
		state.MaxScore = attempt.MaxScore
		journey.Stages[StageWritten] = state
	}
	if err := SaveJourneyStages(ctx, journey); err != nil {
		return nil, err
	}

	return attempt, nil
}

// scoreFreeText isolates the nil-scorer case so SubmitAttempt reads cleanly.
func scoreFreeText(
	ctx context.Context,
	scorer Scorer,
	requests []FreeTextRequest,
) (FreeTextResult, error) {
	if scorer == nil {
		return FreeTextResult{}, errors.New("no scorer configured")
	}
	return scorer.ScoreFreeText(ctx, requests)
}

// ValidateSlot checks assessor-published availability.
func ValidateSlot(slot Slot) error {
	if slot.Stage != StageOral && slot.Stage != StagePractical {
		return ErrInvalidStage
	}
	if slot.StartsAt.IsZero() || slot.EndsAt.IsZero() {
		return errors.New("slot start and end times are required")
	}
	if !slot.EndsAt.After(slot.StartsAt) {
		return errors.New("slot end time must be after the start time")
	}
	if slot.Stage == StagePractical && strings.TrimSpace(slot.CentreID) == "" {
		return ErrCentreNeeded
	}
	return nil
}

// ValidateReport checks that a report submission has the required fields.
// The report payload has no artisan_id or skill_id; the server derives those
// from the appointment.
func ValidateReport(score, maxScore int, notes string) error {
	if maxScore <= 0 {
		return errors.New("max score must be positive")
	}
	if score < 0 || score > maxScore {
		return errors.New("score must be between 0 and max score")
	}
	if strings.TrimSpace(notes) == "" {
		return errors.New("assessor notes are required")
	}
	return nil
}

// FindQualifiedAssessor picks the assessor with fewest upcoming appointments
// who is qualified for the given skill. If no assessor is found it returns
// ErrNotFound. The caller should retry on race conditions (slot claimed
// between lookup and booking).
func FindQualifiedAssessor(ctx context.Context, skillID string, from time.Time) (string, error) {
	skills, err := ListAssessorSkillsBySkill(ctx, skillID)
	if err != nil {
		return "", err
	}
	if len(skills) == 0 {
		return "", ErrNotFound
	}

	type candidate struct {
		assessorID string
		count      int64
	}

	candidates := make([]candidate, 0, len(skills))
	for _, s := range skills {
		count, err := CountUpcomingAppointments(ctx, s.AssessorID, from)
		if err != nil {
			return "", err
		}
		candidates = append(candidates, candidate{assessorID: s.AssessorID, count: count})
	}

	// Sort by fewest upcoming appointments; stable so ties are deterministic.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].count < candidates[j].count
	})

	return candidates[0].assessorID, nil
}
