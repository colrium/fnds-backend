package jobrequests

import (
	"context"
	"regexp"
	"strings"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
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
	ID                   string     `json:"id" bson:"_id"`
	OwnerID              string     `json:"owner_id" bson:"owner_id"`
	CompanyID            string     `json:"company_id" bson:"company_id"`
	Title                string     `json:"title" bson:"title"`
	Level                string     `json:"level" bson:"level"`
	Positions            int        `json:"positions" bson:"positions"`
	EmploymentType       string     `json:"employment_type" bson:"employment_type"`
	Experience           string     `json:"experience" bson:"experience"`
	SalaryRange          string     `json:"salary_range" bson:"salary_range"`
	Skill                string     `json:"skill" bson:"skill"`
	Location             string     `json:"location" bson:"location"`
	Description          string     `json:"description" bson:"description"`
	Responsibilities     string     `json:"responsibilities" bson:"responsibilities"`
	ApplicationStartDate string     `json:"application_start_date" bson:"application_start_date"`
	ApplicationEndDate   string     `json:"application_end_date" bson:"application_end_date"`
	Status               string     `json:"status" bson:"status"`
	IsFeatured           bool       `json:"is_featured" bson:"is_featured"`
	FeaturedAt           *time.Time `json:"featured_at,omitempty" bson:"featured_at,omitempty"`
	// Company snapshot — captured at creation so listings render without a join.
	CompanyName        string    `json:"company_name" bson:"company_name"`
	CompanyWebsite     string    `json:"company_website" bson:"company_website"`
	CompanyLocation    string    `json:"company_location" bson:"company_location"`
	ContactPerson      string    `json:"contact_person" bson:"contact_person"`
	ContactPersonPhone string    `json:"contact_person_phone" bson:"contact_person_phone"`
	ContactPersonEmail string    `json:"contact_person_email" bson:"contact_person_email"`
	CreatedAt          time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt          time.Time `json:"updated_at" bson:"updated_at"`
}

func EnsureIndexes(ctx context.Context) error {
	_, err := configs.MI.DB.Collection("job_applications").Indexes().CreateOne(
		ctx,
		mongo.IndexModel{
			Keys: bson.D{
				{Key: "applicant_id", Value: 1},
				{Key: "vacancy_id", Value: 1},
			},
			Options: options.Index().
				SetName("uniq_applicant_vacancy").
				SetUnique(true),
		},
	)
	// Performance-only indexes are applied operationally. Building them during
	// startup can make the API unavailable when MongoDB is near its disk limit.
	return err
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

// EmployerDashboard is the analytics + recent-activity payload for an employer's
// landing page.
type EmployerDashboard struct {
	TotalVacancies       int          `json:"total_vacancies"`
	OpenVacancies        int          `json:"open_vacancies"`
	ClosedVacancies      int          `json:"closed_vacancies"`
	TotalApplications    int          `json:"total_applications"`
	PendingApplications  int          `json:"pending_applications"`
	AcceptedApplications int          `json:"accepted_applications"`
	RejectedApplications int          `json:"rejected_applications"`
	RecentVacancies      []JobVacancy `json:"recent_vacancies"`
}

// JobApplicationWithApplicant enriches an application with the applicant's
// current profile, resolved at read time so contact details stay fresh.
type JobApplicationWithApplicant struct {
	JobApplication
	ApplicantName  string `json:"applicant_name"`
	ApplicantEmail string `json:"applicant_email"`
	ApplicantPhone string `json:"applicant_phone"`
}

type Company struct {
	ID                 string    `json:"id" bson:"_id"`
	OwnerID            string    `json:"owner_id" bson:"owner_id"`
	Name               string    `json:"name" bson:"name"`
	Website            string    `json:"website" bson:"website"`
	Location           string    `json:"location" bson:"location"`
	ContactPerson      string    `json:"contact_person" bson:"contact_person"`
	ContactPersonPhone string    `json:"contact_person_phone" bson:"contact_person_phone"`
	ContactPersonEmail string    `json:"contact_person_email" bson:"contact_person_email"`
	CreatedAt          time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt          time.Time `json:"updated_at" bson:"updated_at"`
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

// ClaimJobRequest atomically assigns an unclaimed pending request to an artisan.
// Returns false when no matching pending+unassigned request exists (already taken).
func ClaimJobRequest(id, artisanID string) (bool, error) {
	now := time.Now()
	res, err := configs.MI.DB.Collection("job_requests").UpdateOne(
		context.TODO(),
		bson.M{"_id": id, "status": "pending", "artisan_id": ""},
		bson.M{"$set": bson.M{"artisan_id": artisanID, "status": "accepted", "accepted_at": now, "updated_at": now}},
	)
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// CompleteJobRequest atomically completes a request owned by the artisan and in accepted state.
// Returns false when no matching request exists (wrong owner or state).
func CompleteJobRequest(id, artisanID string) (bool, error) {
	now := time.Now()
	res, err := configs.MI.DB.Collection("job_requests").UpdateOne(
		context.TODO(),
		bson.M{"_id": id, "artisan_id": artisanID, "status": "accepted"},
		bson.M{"$set": bson.M{"status": "completed", "completed_at": now, "updated_at": now}},
	)
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

func ListJobVacancies(filter bson.M, page, limit int64) ([]JobVacancy, int64, error) {
	opts := options.Find().
		SetSkip((page - 1) * limit).
		SetLimit(limit).
		SetSort(bson.D{{Key: "is_featured", Value: -1}, {Key: "created_at", Value: -1}})
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

// ListJobVacanciesByOwner returns all of an owner's vacancies (newest first),
// unpaginated — used for dashboard analytics and recent-activity lists.
func ListJobVacanciesByOwner(ownerID string) ([]JobVacancy, error) {
	opts := options.Find().SetSort(bson.M{"created_at": -1})
	cursor, err := configs.MI.DB.Collection("job_vacancies").Find(context.TODO(), bson.M{"owner_id": ownerID}, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())

	vacancies := []JobVacancy{}
	if err := cursor.All(context.TODO(), &vacancies); err != nil {
		return nil, err
	}
	return vacancies, nil
}

// CountApplicationsByStatusForVacancies returns, for the given vacancy IDs, a map
// of application status -> count. Empty input yields an empty map.
func CountApplicationsByStatusForVacancies(vacancyIDs []string) (map[string]int, error) {
	counts := map[string]int{}
	if len(vacancyIDs) == 0 {
		return counts, nil
	}
	cursor, err := configs.MI.DB.Collection("job_applications").Find(
		context.TODO(),
		bson.M{"vacancy_id": bson.M{"$in": vacancyIDs}},
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())

	applications := []JobApplication{}
	if err := cursor.All(context.TODO(), &applications); err != nil {
		return nil, err
	}
	for _, a := range applications {
		counts[a.Status]++
	}
	return counts, nil
}

func GetJobVacancyByID(id string) (*JobVacancy, error) {
	var v JobVacancy
	err := configs.MI.DB.Collection("job_vacancies").FindOne(context.TODO(), bson.M{"_id": id}).Decode(&v)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func GetJobVacancyForOwner(id, ownerID string) (*JobVacancy, error) {
	var v JobVacancy
	err := configs.MI.DB.Collection("job_vacancies").
		FindOne(context.TODO(), bson.M{"_id": id, "owner_id": ownerID}).
		Decode(&v)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// UpdateJobVacancyForOwner applies updates only when the vacancy belongs to ownerID.
// Returns false when no matching vacancy exists (wrong owner or missing).
func UpdateJobVacancyForOwner(id, ownerID string, updates bson.M) (bool, error) {
	updates["updated_at"] = time.Now()
	res, err := configs.MI.DB.Collection("job_vacancies").UpdateOne(
		context.TODO(),
		bson.M{"_id": id, "owner_id": ownerID},
		bson.M{"$set": updates},
	)
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// DeleteJobVacancyForOwner removes a vacancy only when it belongs to ownerID.
// Returns false when no matching vacancy exists (wrong owner or missing).
func DeleteJobVacancyForOwner(id, ownerID string) (bool, error) {
	res, err := configs.MI.DB.Collection("job_vacancies").DeleteOne(
		context.TODO(),
		bson.M{"_id": id, "owner_id": ownerID},
	)
	if err != nil {
		return false, err
	}
	return res.DeletedCount > 0, nil
}

func CreateJobApplication(a *JobApplication) error {
	a.ID = uuid.New().String()
	a.CreatedAt = time.Now()
	a.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("job_applications").InsertOne(context.TODO(), a)
	return err
}

func HasJobApplication(applicantID, vacancyID string) (bool, error) {
	count, err := configs.MI.DB.Collection("job_applications").CountDocuments(
		context.TODO(),
		bson.M{"applicant_id": applicantID, "vacancy_id": vacancyID},
	)
	return count > 0, err
}

func GetJobApplicationByID(id string) (*JobApplication, error) {
	var a JobApplication
	err := configs.MI.DB.Collection("job_applications").FindOne(context.TODO(), bson.M{"_id": id}).Decode(&a)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func ListJobApplicationsByVacancy(vacancyID string) ([]JobApplication, error) {
	opts := options.Find().SetSort(bson.M{"created_at": -1})
	cursor, err := configs.MI.DB.Collection("job_applications").Find(context.TODO(), bson.M{"vacancy_id": vacancyID}, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())

	applications := []JobApplication{}
	if err := cursor.All(context.TODO(), &applications); err != nil {
		return nil, err
	}
	return applications, nil
}

func UpdateJobApplicationStatus(id, status string) (bool, error) {
	res, err := configs.MI.DB.Collection("job_applications").UpdateOne(
		context.TODO(),
		bson.M{"_id": id, "status": ApplicationStatusPending},
		bson.M{"$set": bson.M{"status": status, "updated_at": time.Now()}},
	)
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

func CreateCompany(c *Company) error {
	c.ID = uuid.New().String()
	c.CreatedAt = time.Now()
	c.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("companies").InsertOne(context.TODO(), c)
	return err
}

// GetCompanyByName finds an owner's company by case-insensitive exact name.
// Returns (nil, nil) when none exists so callers can distinguish "not found"
// from a real error.
func GetCompanyByName(ownerID, name string) (*Company, error) {
	trimmed := strings.TrimSpace(name)
	pattern := "^" + regexp.QuoteMeta(trimmed) + "$"
	var c Company
	err := configs.MI.DB.Collection("companies").FindOne(
		context.TODO(),
		bson.M{"owner_id": ownerID, "name": bson.M{"$regex": pattern, "$options": "i"}},
	).Decode(&c)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func ListCompaniesByOwner(ownerID string) ([]Company, error) {
	opts := options.Find().SetSort(bson.M{"created_at": -1})
	cursor, err := configs.MI.DB.Collection("companies").Find(context.TODO(), bson.M{"owner_id": ownerID}, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())

	companies := []Company{}
	if err := cursor.All(context.TODO(), &companies); err != nil {
		return nil, err
	}
	return companies, nil
}

// GetCompanyForOwner fetches a company by id but only if it belongs to ownerID,
// so one employer can never read or reuse another's company.
func GetCompanyForOwner(id, ownerID string) (*Company, error) {
	var c Company
	err := configs.MI.DB.Collection("companies").
		FindOne(context.TODO(), bson.M{"_id": id, "owner_id": ownerID}).
		Decode(&c)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpdateCompanyForOwner applies updates only when the company belongs to ownerID.
// Returns false when no matching company exists (wrong owner or missing).
func UpdateCompanyForOwner(id, ownerID string, updates bson.M) (bool, error) {
	updates["updated_at"] = time.Now()
	res, err := configs.MI.DB.Collection("companies").UpdateOne(
		context.TODO(),
		bson.M{"_id": id, "owner_id": ownerID},
		bson.M{"$set": updates},
	)
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// DeleteCompanyForOwner removes a company only when it belongs to ownerID.
// Returns false when no matching company exists (wrong owner or missing).
func DeleteCompanyForOwner(id, ownerID string) (bool, error) {
	res, err := configs.MI.DB.Collection("companies").DeleteOne(
		context.TODO(),
		bson.M{"_id": id, "owner_id": ownerID},
	)
	if err != nil {
		return false, err
	}
	return res.DeletedCount > 0, nil
}
