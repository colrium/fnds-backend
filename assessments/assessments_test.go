package assessments

import (
	"testing"
	"time"
)

func TestStageOrder(t *testing.T) {
	t.Parallel()

	order := StageOrder()
	if len(order) != 5 {
		t.Fatalf("StageOrder() returned %d stages, want 5", len(order))
	}

	expected := []Stage{
		StageProfileReview,
		StageOral,
		StageWritten,
		StagePractical,
		StageCertified,
	}
	for i, stage := range expected {
		if order[i] != stage {
			t.Errorf("StageOrder()[%d] = %q, want %q", i, order[i], stage)
		}
	}
}

func TestNextStage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		stage     Stage
		wantNext  Stage
		wantFound bool
	}{
		{
			name:      "profile_review advances to oral",
			stage:     StageProfileReview,
			wantNext:  StageOral,
			wantFound: true,
		},
		{
			name:      "oral advances to written",
			stage:     StageOral,
			wantNext:  StageWritten,
			wantFound: true,
		},
		{
			name:      "written advances to practical",
			stage:     StageWritten,
			wantNext:  StagePractical,
			wantFound: true,
		},
		{
			name:      "practical advances to certified",
			stage:     StagePractical,
			wantNext:  StageCertified,
			wantFound: true,
		},
		{
			name:      "certified is terminal",
			stage:     StageCertified,
			wantNext:  "",
			wantFound: false,
		},
		{
			name:      "unknown stage returns false",
			stage:     Stage("nonexistent"),
			wantNext:  "",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			next, found := NextStage(tt.stage)
			if found != tt.wantFound {
				t.Fatalf("NextStage(%q) found = %v, want %v", tt.stage, found, tt.wantFound)
			}
			if next != tt.wantNext {
				t.Fatalf("NextStage(%q) = %q, want %q", tt.stage, next, tt.wantNext)
			}
		})
	}
}

func TestNewJourneyStages(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	stages := NewJourneyStages(now)

	if len(stages) != 5 {
		t.Fatalf("NewJourneyStages() returned %d stages, want 5", len(stages))
	}

	// Profile review should be available.
	if stages[StageProfileReview].Status != StatusAvailable {
		t.Errorf("profile_review status = %q, want %q", stages[StageProfileReview].Status, StatusAvailable)
	}

	// All other stages should be locked.
	lockedStages := []Stage{StageOral, StageWritten, StagePractical, StageCertified}
	for _, stage := range lockedStages {
		if stages[stage].Status != StatusLocked {
			t.Errorf("stage %q status = %q, want %q", stage, stages[stage].Status, StatusLocked)
		}
	}

	// All stages should have UpdatedAt set to now.
	for _, stage := range StageOrder() {
		if !stages[stage].UpdatedAt.Equal(now) {
			t.Errorf("stage %q UpdatedAt = %v, want %v", stage, stages[stage].UpdatedAt, now)
		}
	}
}

func TestApplyDecision(t *testing.T) {
	t.Parallel()

	// Use a Wednesday so the 48h cooldown doesn't hit a weekend boundary.
	now := time.Date(2024, 6, 12, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name            string
		stage           Stage
		passed          bool
		wantStageStatus StageStatus
		wantNextStatus  StageStatus
		wantCooldown    bool
		wantCooldownDur time.Duration
	}{
		{
			name:            "pass advances to next stage",
			stage:           StageOral,
			passed:          true,
			wantStageStatus: StatusPassed,
			wantNextStatus:  StatusAvailable,
		},
		{
			name:            "fail sets failed status",
			stage:           StageOral,
			passed:          false,
			wantStageStatus: StatusFailed,
		},
		{
			name:            "written pass advances to practical",
			stage:           StageWritten,
			passed:          true,
			wantStageStatus: StatusPassed,
			wantNextStatus:  StatusAvailable,
		},
		{
			name:            "written fail sets 48h cooldown",
			stage:           StageWritten,
			passed:          false,
			wantStageStatus: StatusFailed,
			wantCooldown:    true,
			wantCooldownDur: 48 * time.Hour,
		},
		{
			name:            "practical pass certifies",
			stage:           StagePractical,
			passed:          true,
			wantStageStatus: StatusPassed,
			wantNextStatus:  StatusPassed, // certified is terminal outcome
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			journey := &Journey{
				ID:           "journey-1",
				UserID:       "user-1",
				CurrentStage: tt.stage,
				Stages:       NewJourneyStages(now.Add(-24 * time.Hour)),
			}
			// Unlock the target stage so ApplyDecision can work.
			state := journey.Stages[tt.stage]
			state.Status = StatusInProgress
			journey.Stages[tt.stage] = state

			err := ApplyDecision(journey, tt.stage, tt.passed, "admin-1", "notes", "feedback", now)
			if err != nil {
				t.Fatalf("ApplyDecision() error = %v", err)
			}

			gotState := journey.Stages[tt.stage]
			if gotState.Status != tt.wantStageStatus {
				t.Errorf("stage %q status = %q, want %q", tt.stage, gotState.Status, tt.wantStageStatus)
			}

			if tt.passed && tt.wantNextStatus != "" {
				next, _ := NextStage(tt.stage)
				nextState := journey.Stages[next]
				if nextState.Status != tt.wantNextStatus {
					t.Errorf("next stage %q status = %q, want %q", next, nextState.Status, tt.wantNextStatus)
				}
			}

			if tt.wantCooldown {
				if gotState.NextAttemptAt == nil {
					t.Fatal("expected NextAttemptAt to be set")
				}
				expectedTime := now.Add(tt.wantCooldownDur)
				if !gotState.NextAttemptAt.Equal(expectedTime) {
					t.Errorf("NextAttemptAt = %v, want %v", *gotState.NextAttemptAt, expectedTime)
				}
			}
		})
	}
}

func TestCanAttemptStage(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		status  StageStatus
		nextAt  *time.Time
		wantOK  bool
		wantErr error
	}{
		{
			name:   "available stage can be attempted",
			status: StatusAvailable,
			wantOK: true,
		},
		{
			name:   "in_progress stage can be attempted",
			status: StatusInProgress,
			wantOK: true,
		},
		{
			name:    "locked stage cannot be attempted",
			status:  StatusLocked,
			wantOK:  false,
			wantErr: ErrStageNotAvailable,
		},
		{
			name:    "failed with active cooldown cannot be attempted",
			status:  StatusFailed,
			nextAt:  timePtr(now.Add(24 * time.Hour)),
			wantOK:  false,
			wantErr: ErrCooldownActive,
		},
		{
			name:   "failed after cooldown can be attempted",
			status: StatusFailed,
			nextAt: timePtr(now.Add(-1 * time.Hour)),
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			journey := &Journey{
				ID:     "journey-1",
				Stages: NewJourneyStages(now.Add(-48 * time.Hour)),
			}
			state := journey.Stages[StageWritten]
			state.Status = tt.status
			state.NextAttemptAt = tt.nextAt
			journey.Stages[StageWritten] = state

			ok, _, err := CanAttemptStage(journey, StageWritten, now)
			if ok != tt.wantOK {
				t.Errorf("CanAttemptStage() ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tt.wantErr)
				}
				if err != tt.wantErr {
					t.Errorf("error = %v, want %v", err, tt.wantErr)
				}
			}
		})
	}
}

func TestScoreObjective(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		questions    []AttemptQuestion
		answers      map[string]AttemptAnswer
		wantScore    int
		wantMax      int
		wantFreeText int
	}{
		{
			name: "correct answers score",
			questions: []AttemptQuestion{
				{QuestionID: "q1", Type: QuestionSingleChoice, Points: 2, CorrectOptionIDs: []string{"a"}},
				{QuestionID: "q2", Type: QuestionMultiChoice, Points: 3, CorrectOptionIDs: []string{"b", "c"}},
			},
			answers: map[string]AttemptAnswer{
				"q1": {QuestionID: "q1", OptionIDs: []string{"a"}},
				"q2": {QuestionID: "q2", OptionIDs: []string{"c", "b"}},
			},
			wantScore: 5,
			wantMax:   5,
		},
		{
			name: "wrong answers do not score",
			questions: []AttemptQuestion{
				{QuestionID: "q1", Type: QuestionSingleChoice, Points: 2, CorrectOptionIDs: []string{"a"}},
				{QuestionID: "q2", Type: QuestionMultiChoice, Points: 3, CorrectOptionIDs: []string{"b", "c"}},
			},
			answers: map[string]AttemptAnswer{
				"q1": {QuestionID: "q1", OptionIDs: []string{"wrong"}},
				"q2": {QuestionID: "q2", OptionIDs: []string{"b"}}, // incomplete set
			},
			wantScore: 0,
			wantMax:   5,
		},
		{
			name: "free text questions are deferred",
			questions: []AttemptQuestion{
				{QuestionID: "q1", Type: QuestionFreeText, Points: 5},
				{QuestionID: "q2", Type: QuestionSingleChoice, Points: 2, CorrectOptionIDs: []string{"a"}},
			},
			answers: map[string]AttemptAnswer{
				"q1": {QuestionID: "q1", Text: "my answer"},
				"q2": {QuestionID: "q2", OptionIDs: []string{"a"}},
			},
			wantScore:    2,
			wantMax:      7,
			wantFreeText: 1,
		},
		{
			name: "unknown questions are ignored in answers",
			questions: []AttemptQuestion{
				{QuestionID: "q1", Type: QuestionSingleChoice, Points: 2, CorrectOptionIDs: []string{"a"}},
			},
			answers: map[string]AttemptAnswer{
				"q1":      {QuestionID: "q1", OptionIDs: []string{"a"}},
				"unknown": {QuestionID: "unknown", OptionIDs: []string{"x"}},
			},
			wantScore: 2,
			wantMax:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := ScoreObjective(tt.questions, tt.answers)
			if result.Score != tt.wantScore {
				t.Errorf("Score = %d, want %d", result.Score, tt.wantScore)
			}
			if result.MaxScore != tt.wantMax {
				t.Errorf("MaxScore = %d, want %d", result.MaxScore, tt.wantMax)
			}
			if len(result.FreeText) != tt.wantFreeText {
				t.Errorf("FreeText count = %d, want %d", len(result.FreeText), tt.wantFreeText)
			}
		})
	}
}

func TestPassed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		score       int
		maxScore    int
		passPercent int
		want        bool
	}{
		{
			name:        "exactly at threshold passes",
			score:       80,
			maxScore:    100,
			passPercent: 80,
			want:        true,
		},
		{
			name:        "above threshold passes",
			score:       90,
			maxScore:    100,
			passPercent: 80,
			want:        true,
		},
		{
			name:        "below threshold fails",
			score:       79,
			maxScore:    100,
			passPercent: 80,
			want:        false,
		},
		{
			name:        "zero max never passes",
			score:       0,
			maxScore:    0,
			passPercent: 80,
			want:        false,
		},
		{
			name:        "negative max never passes",
			score:       10,
			maxScore:    -1,
			passPercent: 80,
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Passed(tt.score, tt.maxScore, tt.passPercent)
			if got != tt.want {
				t.Errorf("Passed(%d, %d, %d) = %v, want %v", tt.score, tt.maxScore, tt.passPercent, got, tt.want)
			}
		})
	}
}

func TestIntegrityFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		attempt   *Attempt
		wantFlags []string
	}{
		{
			name:      "nil attempt returns nil",
			attempt:   nil,
			wantFlags: nil,
		},
		{
			name: "pastes are flagged",
			attempt: &Attempt{
				Answers: map[string]AttemptAnswer{
					"q1": {PasteCount: 3},
					"q2": {PasteCount: 1},
				},
			},
			wantFlags: []string{"pasted content 4 time(s)"},
		},
		{
			name: "more than 3 blurs are flagged",
			attempt: &Attempt{
				Answers: map[string]AttemptAnswer{
					"q1": {BlurCount: 2},
					"q2": {BlurCount: 2},
				},
			},
			wantFlags: []string{"left the page 4 time(s)"},
		},
		{
			name: "3 or fewer blurs are not flagged",
			attempt: &Attempt{
				Answers: map[string]AttemptAnswer{
					"q1": {BlurCount: 2},
					"q2": {BlurCount: 1},
				},
			},
			wantFlags: []string{},
		},
		{
			name: "instant answers are flagged",
			attempt: &Attempt{
				Answers: map[string]AttemptAnswer{
					"q1": {DwellMS: 500},
					"q2": {DwellMS: 1999},
					"q3": {DwellMS: 2000}, // exactly 2s is not instant
				},
			},
			wantFlags: []string{"answered 2 question(s) in under 2s"},
		},
		{
			name: "pauses are reported",
			attempt: &Attempt{
				PauseCount: 3,
				Answers:    map[string]AttemptAnswer{},
			},
			wantFlags: []string{"paused 3 time(s)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			flags := IntegrityFlags(tt.attempt)
			if tt.wantFlags == nil {
				if flags != nil {
					t.Fatalf("IntegrityFlags() = %v, want nil", flags)
				}
				return
			}
			if len(flags) != len(tt.wantFlags) {
				t.Fatalf("IntegrityFlags() = %v, want %v", flags, tt.wantFlags)
			}
			for i, flag := range tt.wantFlags {
				if flags[i] != flag {
					t.Errorf("flags[%d] = %q, want %q", i, flags[i], flag)
				}
			}
		})
	}
}

func TestBuildAttemptQuestions(t *testing.T) {
	t.Parallel()

	questions := []Question{
		{
			ID:               "q1",
			Type:             QuestionSingleChoice,
			Prompt:           "What is X?",
			Options:          []QuestionOption{{ID: "a", Text: "A"}, {ID: "b", Text: "B"}},
			CorrectOptionIDs: []string{"a"},
			Points:           2,
			Rubric:           "test rubric",
		},
		{
			ID:               "q2",
			Type:             QuestionFreeText,
			Prompt:           "Explain Y.",
			Options:          nil,
			CorrectOptionIDs: nil,
			Points:           5,
			Rubric:           "explain clearly",
		},
	}

	// Use a no-op shuffle so output is deterministic.
	noShuffle := func(n int, swap func(i, j int)) {}

	built := BuildAttemptQuestions(questions, noShuffle)

	if len(built) != 2 {
		t.Fatalf("BuildAttemptQuestions() returned %d questions, want 2", len(built))
	}

	// First question preserves data.
	if built[0].QuestionID != "q1" {
		t.Errorf("built[0].QuestionID = %q, want %q", built[0].QuestionID, "q1")
	}
	if built[0].Type != QuestionSingleChoice {
		t.Errorf("built[0].Type = %q, want %q", built[0].Type, QuestionSingleChoice)
	}
	if built[0].Prompt != "What is X?" {
		t.Errorf("built[0].Prompt = %q, want %q", built[0].Prompt, "What is X?")
	}
	if built[0].Points != 2 {
		t.Errorf("built[0].Points = %d, want 2", built[0].Points)
	}
	if len(built[0].Options) != 2 {
		t.Fatalf("built[0].Options has %d entries, want 2", len(built[0].Options))
	}
	if len(built[0].CorrectOptionIDs) != 1 || built[0].CorrectOptionIDs[0] != "a" {
		t.Errorf("built[0].CorrectOptionIDs = %v, want [a]", built[0].CorrectOptionIDs)
	}
	if built[0].Rubric != "test rubric" {
		t.Errorf("built[0].Rubric = %q, want %q", built[0].Rubric, "test rubric")
	}

	// Second question (free text).
	if built[1].QuestionID != "q2" {
		t.Errorf("built[1].QuestionID = %q, want %q", built[1].QuestionID, "q2")
	}
	if built[1].Points != 5 {
		t.Errorf("built[1].Points = %d, want 5", built[1].Points)
	}

	// Verify that shuffle is actually called by testing with a reversing shuffle.
	reverseShuf := func(n int, swap func(i, j int)) {
		for i := 0; i < n/2; i++ {
			swap(i, n-1-i)
		}
	}
	built2 := BuildAttemptQuestions(questions[:1], reverseShuf)
	if len(built2[0].Options) != 2 {
		t.Fatalf("expected 2 options after reverse shuffle")
	}
	// With reverse, [A, B] becomes [B, A].
	if built2[0].Options[0].ID != "b" || built2[0].Options[1].ID != "a" {
		t.Errorf("shuffle did not reverse options: got %v", built2[0].Options)
	}
}

func TestSameOptionSet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		expected []string
		actual   []string
		want     bool
	}{
		{
			name:     "same single element",
			expected: []string{"a"},
			actual:   []string{"a"},
			want:     true,
		},
		{
			name:     "order independent",
			expected: []string{"a", "b", "c"},
			actual:   []string{"c", "a", "b"},
			want:     true,
		},
		{
			name:     "deduplicates",
			expected: []string{"a", "b"},
			actual:   []string{"a", "b", "b"},
			want:     true,
		},
		{
			name:     "different sets",
			expected: []string{"a", "b"},
			actual:   []string{"a", "c"},
			want:     false,
		},
		{
			name:     "empty expected always false",
			expected: []string{},
			actual:   []string{"a"},
			want:     false,
		},
		{
			name:     "nil expected always false",
			expected: nil,
			actual:   []string{"a"},
			want:     false,
		},
		{
			name:     "both empty is false",
			expected: []string{},
			actual:   []string{},
			want:     false,
		},
		{
			name:     "handles whitespace",
			expected: []string{"a", "b"},
			actual:   []string{" a ", " b "},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sameOptionSet(tt.expected, tt.actual)
			if got != tt.want {
				t.Errorf("sameOptionSet(%v, %v) = %v, want %v", tt.expected, tt.actual, got, tt.want)
			}
		})
	}
}

func TestValidateSlot(t *testing.T) {
	t.Parallel()

	validStart := time.Date(2024, 6, 15, 9, 0, 0, 0, time.UTC)
	validEnd := time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		slot    Slot
		wantErr bool
	}{
		{
			name: "valid oral slot",
			slot: Slot{
				Stage:    StageOral,
				StartsAt: validStart,
				EndsAt:   validEnd,
			},
			wantErr: false,
		},
		{
			name: "valid practical slot with centre",
			slot: Slot{
				Stage:    StagePractical,
				StartsAt: validStart,
				EndsAt:   validEnd,
				CentreID: "centre-1",
			},
			wantErr: false,
		},
		{
			name: "missing start time",
			slot: Slot{
				Stage:  StageOral,
				EndsAt: validEnd,
			},
			wantErr: true,
		},
		{
			name: "missing end time",
			slot: Slot{
				Stage:    StageOral,
				StartsAt: validStart,
			},
			wantErr: true,
		},
		{
			name: "end before start",
			slot: Slot{
				Stage:    StageOral,
				StartsAt: validEnd,
				EndsAt:   validStart,
			},
			wantErr: true,
		},
		{
			name: "practical needs centre",
			slot: Slot{
				Stage:    StagePractical,
				StartsAt: validStart,
				EndsAt:   validEnd,
				CentreID: "",
			},
			wantErr: true,
		},
		{
			name: "invalid stage",
			slot: Slot{
				Stage:    StageWritten,
				StartsAt: validStart,
				EndsAt:   validEnd,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateSlot(tt.slot)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSlot() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateReport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		score    int
		maxScore int
		notes    string
		wantErr  bool
	}{
		{
			name:     "valid report",
			score:    80,
			maxScore: 100,
			notes:    "Good performance",
			wantErr:  false,
		},
		{
			name:     "missing grade (max score zero)",
			score:    0,
			maxScore: 0,
			notes:    "some notes",
			wantErr:  true,
		},
		{
			name:     "missing score (negative)",
			score:    -1,
			maxScore: 100,
			notes:    "some notes",
			wantErr:  true,
		},
		{
			name:     "score exceeds max",
			score:    101,
			maxScore: 100,
			notes:    "some notes",
			wantErr:  true,
		},
		{
			name:     "invalid category (empty notes)",
			score:    50,
			maxScore: 100,
			notes:    "",
			wantErr:  true,
		},
		{
			name:     "whitespace-only notes",
			score:    50,
			maxScore: 100,
			notes:    "   ",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateReport(tt.score, tt.maxScore, tt.notes)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateReport(%d, %d, %q) error = %v, wantErr = %v",
					tt.score, tt.maxScore, tt.notes, err, tt.wantErr)
			}
		})
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}

func TestAddBusinessHours(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		from     time.Time
		duration time.Duration
		want     time.Time
	}{
		{
			name:     "weekday no weekend boundary",
			from:     time.Date(2024, 6, 12, 10, 0, 0, 0, time.UTC), // Wednesday
			duration: 24 * time.Hour,
			want:     time.Date(2024, 6, 13, 10, 0, 0, 0, time.UTC), // Thursday
		},
		{
			name:     "crosses weekend from Friday",
			from:     time.Date(2024, 6, 14, 10, 0, 0, 0, time.UTC), // Friday 10:00
			duration: 48 * time.Hour,
			// Friday 10:00 has 14h left -> Saturday skipped -> Sunday skipped -> Monday starts with 34h remaining
			// Monday 00:00 + 34h = Tuesday 10:00
			want: time.Date(2024, 6, 18, 10, 0, 0, 0, time.UTC), // Tuesday 10:00
		},
		{
			name:     "starts on Saturday",
			from:     time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC), // Saturday
			duration: 48 * time.Hour,
			// Skips to Monday, then 48h = Wednesday 00:00
			want: time.Date(2024, 6, 19, 0, 0, 0, 0, time.UTC), // Wednesday
		},
		{
			name:     "zero duration",
			from:     time.Date(2024, 6, 12, 10, 0, 0, 0, time.UTC),
			duration: 0,
			want:     time.Date(2024, 6, 12, 10, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := AddBusinessHours(tt.from, tt.duration)
			if !got.Equal(tt.want) {
				t.Errorf("AddBusinessHours() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyDecisionWeekendCooldown(t *testing.T) {
	t.Parallel()

	// Friday 10:00 UTC - the 48h cooldown should skip the weekend
	now := time.Date(2024, 6, 14, 10, 0, 0, 0, time.UTC)

	journey := &Journey{
		ID:           "journey-1",
		UserID:       "user-1",
		CurrentStage: StageWritten,
		Stages:       NewJourneyStages(now.Add(-24 * time.Hour)),
	}
	state := journey.Stages[StageWritten]
	state.Status = StatusInProgress
	journey.Stages[StageWritten] = state

	err := ApplyDecision(journey, StageWritten, false, "admin-1", "notes", "feedback", now)
	if err != nil {
		t.Fatalf("ApplyDecision() error = %v", err)
	}

	gotState := journey.Stages[StageWritten]
	if gotState.NextAttemptAt == nil {
		t.Fatal("expected NextAttemptAt to be set")
	}

	// Friday 10:00 + 48 business hours = Tuesday 10:00 (skipping Sat+Sun)
	expected := time.Date(2024, 6, 18, 10, 0, 0, 0, time.UTC)
	if !gotState.NextAttemptAt.Equal(expected) {
		t.Errorf("NextAttemptAt = %v, want %v (weekend should be excluded)", *gotState.NextAttemptAt, expected)
	}
}
