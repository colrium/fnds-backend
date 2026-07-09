package assessments

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

type Assessment struct {
	ID           string    `json:"id" bson:"_id"`
	Title        string    `json:"title" bson:"title"`
	Description  string    `json:"description" bson:"description"`
	TypeID       string    `json:"type_id" bson:"type_id"`
	CourseID     string    `json:"course_id,omitempty" bson:"course_id,omitempty"`
	Duration     int       `json:"duration" bson:"duration"`
	PassingScore int       `json:"passing_score" bson:"passing_score"`
	IsActive     bool      `json:"is_active" bson:"is_active"`
	CreatedAt    time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" bson:"updated_at"`
}

type AssessmentType struct {
	ID          string    `json:"id" bson:"_id"`
	Name        string    `json:"name" bson:"name"`
	Description string    `json:"description" bson:"description"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
}

type AssessmentQuestion struct {
	ID            string   `json:"id" bson:"_id"`
	AssessmentID  string   `json:"assessment_id" bson:"assessment_id"`
	Question      string   `json:"question" bson:"question"`
	Options       []string `json:"options" bson:"options"`
	CorrectAnswer string   `json:"correct_answer" bson:"correct_answer"`
	Points        int      `json:"points" bson:"points"`
	CreatedAt     time.Time `json:"created_at" bson:"created_at"`
}

type AssessmentResponse struct {
	ID           string            `json:"id" bson:"_id"`
	AssessmentID string            `json:"assessment_id" bson:"assessment_id"`
	UserID       string            `json:"user_id" bson:"user_id"`
	Answers      map[string]string `json:"answers" bson:"answers"`
	Score        int               `json:"score" bson:"score"`
	Passed       bool              `json:"passed" bson:"passed"`
	CompletedAt  time.Time         `json:"completed_at" bson:"completed_at"`
	CreatedAt    time.Time         `json:"created_at" bson:"created_at"`
}

type Appointment struct {
	ID            string    `json:"id" bson:"_id"`
	AssessmentID  string    `json:"assessment_id" bson:"assessment_id"`
	UserID        string    `json:"user_id" bson:"user_id"`
	AssessorID    string    `json:"assessor_id,omitempty" bson:"assessor_id,omitempty"`
	TimeSlotID    string    `json:"time_slot_id,omitempty" bson:"time_slot_id,omitempty"`
	Status        string    `json:"status" bson:"status"`
	ScheduledDate string    `json:"scheduled_date" bson:"scheduled_date"`
	ScheduledTime string    `json:"scheduled_time" bson:"scheduled_time"`
	Notes         string    `json:"notes" bson:"notes"`
	CreatedAt     time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" bson:"updated_at"`
}

type TimeSlot struct {
	ID        string    `json:"id" bson:"_id"`
	UserID    string    `json:"user_id" bson:"user_id"`
	Day       string    `json:"day" bson:"day"`
	StartTime string    `json:"start_time" bson:"start_time"`
	EndTime   string    `json:"end_time" bson:"end_time"`
	IsBooked  bool      `json:"is_booked" bson:"is_booked"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
}

type CompetencyAssessment struct {
	ID           string    `json:"id" bson:"_id"`
	UserID       string    `json:"user_id" bson:"user_id"`
	AssessmentID string    `json:"assessment_id" bson:"assessment_id"`
	Score        int       `json:"score" bson:"score"`
	MaxScore     int       `json:"max_score" bson:"max_score"`
	Status       string    `json:"status" bson:"status"`
	CreatedAt    time.Time `json:"created_at" bson:"created_at"`
}

type UpskillingReport struct {
	ID         string    `json:"id" bson:"_id"`
	UserID     string    `json:"user_id" bson:"user_id"`
	CourseID   string    `json:"course_id" bson:"course_id"`
	Progress   int       `json:"progress" bson:"progress"`
	Status     string    `json:"status" bson:"status"`
	CreatedAt  time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt  time.Time `json:"updated_at" bson:"updated_at"`
}

type VettingReport struct {
	ID        string    `json:"id" bson:"_id"`
	UserID    string    `json:"user_id" bson:"user_id"`
	Score     int       `json:"score" bson:"score"`
	MaxScore  int       `json:"max_score" bson:"max_score"`
	Status    string    `json:"status" bson:"status"`
	Notes     string    `json:"notes" bson:"notes"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
}

func GetAllAssessments() ([]Assessment, error) {
	cursor, err := configs.MI.DB.Collection("assessments").Find(context.TODO(), bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var assessments []Assessment
	if err := cursor.All(context.TODO(), &assessments); err != nil {
		return nil, err
	}
	return assessments, nil
}

func GetAllAssessmentTypes() ([]AssessmentType, error) {
	cursor, err := configs.MI.DB.Collection("assessment_types").Find(context.TODO(), bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var types []AssessmentType
	if err := cursor.All(context.TODO(), &types); err != nil {
		return nil, err
	}
	return types, nil
}

func GetAssessmentQuestions(assessmentID string) ([]AssessmentQuestion, error) {
	cursor, err := configs.MI.DB.Collection("assessment_questions").Find(context.TODO(), bson.M{"assessment_id": assessmentID})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var questions []AssessmentQuestion
	if err := cursor.All(context.TODO(), &questions); err != nil {
		return nil, err
	}
	return questions, nil
}

func CreateAssessmentResponse(ar *AssessmentResponse) error {
	ar.ID = uuid.New().String()
	ar.CreatedAt = time.Now()
	_, err := configs.MI.DB.Collection("assessment_responses").InsertOne(context.TODO(), ar)
	return err
}

func ListAppointments(filter bson.M) ([]Appointment, error) {
	cursor, err := configs.MI.DB.Collection("appointments").Find(context.TODO(), filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var appointments []Appointment
	if err := cursor.All(context.TODO(), &appointments); err != nil {
		return nil, err
	}
	return appointments, nil
}

func CreateAppointment(a *Appointment) error {
	a.ID = uuid.New().String()
	a.CreatedAt = time.Now()
	a.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("appointments").InsertOne(context.TODO(), a)
	return err
}

func UpdateAppointment(id string, updates bson.M) error {
	updates["updated_at"] = time.Now()
	_, err := configs.MI.DB.Collection("appointments").UpdateOne(context.TODO(), bson.M{"_id": id}, bson.M{"$set": updates})
	return err
}

func ListTimeSlots(filter bson.M) ([]TimeSlot, error) {
	cursor, err := configs.MI.DB.Collection("time_slots").Find(context.TODO(), filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var slots []TimeSlot
	if err := cursor.All(context.TODO(), &slots); err != nil {
		return nil, err
	}
	return slots, nil
}

func CreateTimeSlot(ts *TimeSlot) error {
	ts.ID = uuid.New().String()
	ts.CreatedAt = time.Now()
	ts.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("time_slots").InsertOne(context.TODO(), ts)
	return err
}

type AssessmentOption struct {
	ID         string `json:"id" bson:"_id"`
	QuestionID string `json:"question_id" bson:"question_id"`
	OptionText string `json:"option_text" bson:"option_text"`
	IsCorrect  bool   `json:"is_correct" bson:"is_correct"`
}

func CreateAssessmentOption(o *AssessmentOption) error {
	o.ID = uuid.New().String()
	_, err := configs.MI.DB.Collection("assessment_options").InsertOne(context.TODO(), o)
	return err
}

func ListOptionsByQuestion(questionID string) ([]AssessmentOption, error) {
	cursor, err := configs.MI.DB.Collection("assessment_options").Find(context.TODO(), bson.M{"question_id": questionID})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var options []AssessmentOption
	if err := cursor.All(context.TODO(), &options); err != nil {
		return nil, err
	}
	return options, nil
}

type AssessmentReport struct {
	ID               string    `json:"id" bson:"_id"`
	ArtisanID        string    `json:"artisan_id" bson:"artisan_id"`
	AssessorID       string    `json:"assessor_id" bson:"assessor_id"`
	SkillID          string    `json:"skill_id" bson:"skill_id"`
	PracticalScore   float64   `json:"practical_score" bson:"practical_score"`
	TheoreticalScore float64   `json:"theoretical_score" bson:"theoretical_score"`
	OralScore        float64   `json:"oral_score" bson:"oral_score"`
	Grade            string    `json:"grade" bson:"grade"`
	Comment          string    `json:"comment" bson:"comment"`
	Recommendation   string    `json:"recommendation" bson:"recommendation"`
	PracticalComment string    `json:"practical_comment" bson:"practical_comment"`
	OralComment      string    `json:"oral_comment" bson:"oral_comment"`
	CreatedAt        time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt        time.Time `json:"updated_at" bson:"updated_at"`
}

func CreateAssessmentReport(ar *AssessmentReport) error {
	ar.ID = uuid.New().String()
	ar.CreatedAt = time.Now()
	ar.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("assessment_reports").InsertOne(context.TODO(), ar)
	return err
}

func ListAssessmentReports(filter bson.M) ([]AssessmentReport, error) {
	cursor, err := configs.MI.DB.Collection("assessment_reports").Find(context.TODO(), filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var reports []AssessmentReport
	if err := cursor.All(context.TODO(), &reports); err != nil {
		return nil, err
	}
	return reports, nil
}
