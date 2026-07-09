package jobrequests

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type JobRequest struct {
	ID                 string     `json:"id" bson:"_id"`
	JobKey             string     `json:"job_key" bson:"job_key"`
	CustomerID         string     `json:"customer_id" bson:"customer_id"`
	ArtisanID          string     `json:"artisan_id,omitempty" bson:"artisan_id,omitempty"`
	Title              string     `json:"title" bson:"title"`
	Description        string     `json:"description" bson:"description"`
	Skills             []string   `json:"skills" bson:"skills"`
	Status             string     `json:"status" bson:"status"`
	Priority           string     `json:"priority" bson:"priority"`
	Budget             float64    `json:"budget" bson:"budget"`
	Location           string     `json:"location" bson:"location"`
	ScheduledDate      string     `json:"scheduled_date" bson:"scheduled_date"`
	ScheduledTime      string     `json:"scheduled_time" bson:"scheduled_time"`
	AcceptedAt         *time.Time `json:"accepted_at,omitempty" bson:"accepted_at,omitempty"`
	CompletedAt        *time.Time `json:"completed_at,omitempty" bson:"completed_at,omitempty"`
	CancelledAt        *time.Time `json:"cancelled_at,omitempty" bson:"cancelled_at,omitempty"`
	CancellationReason string     `json:"cancellation_reason,omitempty" bson:"cancellation_reason,omitempty"`
	IsInsured          bool       `json:"is_insured" bson:"is_insured"`
	CreatedAt          time.Time  `json:"created_at" bson:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at" bson:"updated_at"`
}

type JobVacancy struct {
	ID          string    `json:"id" bson:"_id"`
	CompanyID   string    `json:"company_id" bson:"company_id"`
	Title       string    `json:"title" bson:"title"`
	Description string    `json:"description" bson:"description"`
	Location    string    `json:"location" bson:"location"`
	Salary      string    `json:"salary" bson:"salary"`
	Status      string    `json:"status" bson:"status"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" bson:"updated_at"`
}

type JobApplication struct {
	ID          string    `json:"id" bson:"_id"`
	VacancyID   string    `json:"vacancy_id" bson:"vacancy_id"`
	ApplicantID string    `json:"applicant_id" bson:"applicant_id"`
	Status      string    `json:"status" bson:"status"`
	CoverLetter string    `json:"cover_letter" bson:"cover_letter"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" bson:"updated_at"`
}

type Company struct {
	ID          string    `json:"id" bson:"_id"`
	Name        string    `json:"name" bson:"name"`
	Description string    `json:"description" bson:"description"`
	Logo        string    `json:"logo" bson:"logo"`
	Website     string    `json:"website" bson:"website"`
	OwnerID     string    `json:"owner_id" bson:"owner_id"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" bson:"updated_at"`
}

func CreateJobRequest(jr *JobRequest) error {
	jr.ID = uuid.New().String()
	jr.JobKey = jr.ID[:8]
	jr.CreatedAt = time.Now()
	jr.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("job_requests").InsertOne(context.TODO(), jr)
	return err
}

func GetJobRequestByID(id string) (*JobRequest, error) {
	var jr JobRequest
	err := configs.MI.DB.Collection("job_requests").FindOne(context.TODO(), bson.M{"_id": id}).Decode(&jr)
	if err != nil {
		return nil, err
	}
	return &jr, nil
}

func ListJobRequests(filter bson.M, page, limit int64) ([]JobRequest, int64, error) {
	opts := options.Find().SetSkip((page - 1) * limit).SetLimit(limit).SetSort(bson.M{"created_at": -1})
	cursor, err := configs.MI.DB.Collection("job_requests").Find(context.TODO(), filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close(context.TODO())

	var requests []JobRequest
	if err := cursor.All(context.TODO(), &requests); err != nil {
		return nil, 0, err
	}

	total, err := configs.MI.DB.Collection("job_requests").CountDocuments(context.TODO(), filter)
	if err != nil {
		return nil, 0, err
	}
	return requests, total, nil
}

func UpdateJobRequest(id string, updates bson.M) error {
	updates["updated_at"] = time.Now()
	_, err := configs.MI.DB.Collection("job_requests").UpdateOne(context.TODO(), bson.M{"_id": id}, bson.M{"$set": updates})
	return err
}

func ListJobVacancies(filter bson.M, page, limit int64) ([]JobVacancy, int64, error) {
	opts := options.Find().SetSkip((page - 1) * limit).SetLimit(limit)
	cursor, err := configs.MI.DB.Collection("job_vacancies").Find(context.TODO(), filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close(context.TODO())

	var vacancies []JobVacancy
	if err := cursor.All(context.TODO(), &vacancies); err != nil {
		return nil, 0, err
	}

	total, err := configs.MI.DB.Collection("job_vacancies").CountDocuments(context.TODO(), filter)
	if err != nil {
		return nil, 0, err
	}
	return vacancies, total, nil
}

func CreateJobVacancy(v *JobVacancy) error {
	v.ID = uuid.New().String()
	v.CreatedAt = time.Now()
	v.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("job_vacancies").InsertOne(context.TODO(), v)
	return err
}

func CreateJobApplication(a *JobApplication) error {
	a.ID = uuid.New().String()
	a.CreatedAt = time.Now()
	a.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("job_applications").InsertOne(context.TODO(), a)
	return err
}
