package adminapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"backend/files"
	"backend/profiles"
	"backend/responses"
	"backend/subscriptions"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

const defaultVerificationDuration = 365 * 24 * time.Hour

type artisanPlan struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	PriceKES  int64      `json:"price"`
	Currency  string     `json:"currency"`
	StartDate *time.Time `json:"start_date,omitempty"`
	EndDate   *time.Time `json:"end_date,omitempty"`
}

type artisanListItem struct {
	users.User
	SubscriptionPlans  []artisanPlan `json:"subscription_plans"`
	VerificationActive bool          `json:"verification_active"`
}

type artisanDetail struct {
	*profiles.FullProfile
	SubscriptionPlans   []artisanPlan       `json:"subscription_plans"`
	SubscriptionHistory []artisanPlan       `json:"subscription_history"`
	CV                  *files.FileResponse `json:"cv,omitempty"`
	VerificationActive  bool                `json:"verification_active"`
}

var artisanFileRepository files.Repository
var artisanFileStorage files.Storage

// SetArtisanFileDependencies configures file lookup and presigning for admin
// artisan details and profile reviews. It is called once during startup.
func SetArtisanFileDependencies(repository files.Repository, storage files.Storage) {
	artisanFileRepository = repository
	artisanFileStorage = storage
}

func verificationActive(user users.User, at time.Time) bool {
	return user.IsVerified && user.VerifiedUntil != nil && user.VerifiedUntil.After(at)
}

func planFromSubscription(subscription subscriptions.UserSubscription) artisanPlan {
	name := subscription.SubscriptionPlanID
	if plan, ok := subscriptions.FindPlan(subscription.SubscriptionPlanID); ok {
		name = plan.Name
	}
	startDate := subscription.StartDate
	return artisanPlan{
		ID:        subscription.SubscriptionPlanID,
		Name:      name,
		Status:    subscription.Status,
		PriceKES:  subscription.AmountKES,
		Currency:  subscription.Currency,
		StartDate: &startDate,
		EndDate:   subscription.EndDate,
	}
}

func effectiveArtisanPlans(ctx context.Context, userID string, at time.Time) ([]artisanPlan, error) {
	active, err := subscriptions.GetActiveSubscriptions(ctx, userID, at)
	if err != nil {
		return nil, err
	}

	plans := make([]artisanPlan, 0, len(active)+1)
	hasApplicationPlan := false
	for _, subscription := range active {
		plans = append(plans, planFromSubscription(subscription))
		if plan, ok := subscriptions.FindPlan(subscription.SubscriptionPlanID); ok &&
			plan.Group == subscriptions.GroupWorkerApplications {
			hasApplicationPlan = true
		}
	}

	// The free worker plan is an automatic entitlement and therefore has no
	// user_subscriptions row. Include it whenever no paid application plan is active.
	if !hasApplicationPlan {
		if plan, ok := subscriptions.FindPlan("worker_free"); ok {
			free := artisanPlan{
				ID:       plan.ID,
				Name:     plan.Name,
				Status:   subscriptions.StatusActive,
				PriceKES: plan.PriceKES,
				Currency: plan.Currency,
			}
			plans = append([]artisanPlan{free}, plans...)
		}
	}
	return plans, nil
}

func artisanSubscriptionHistory(ctx context.Context, userID string, at time.Time) ([]artisanPlan, error) {
	history, err := subscriptions.GetSubscriptionHistory(ctx, userID)
	if err != nil {
		return nil, err
	}
	plans := make([]artisanPlan, 0, len(history))
	for _, subscription := range history {
		if subscription.Status == subscriptions.StatusActive &&
			subscription.EndDate != nil && !subscription.EndDate.After(at) {
			subscription.Status = subscriptions.StatusExpired
		}
		plans = append(plans, planFromSubscription(subscription))
	}
	return plans, nil
}

func buildArtisanFilter(status, search, verification string, at time.Time) bson.M {
	filter := buildUserFilter("artisan", status, search)
	switch verification {
	case "verified":
		filter["$and"] = []bson.M{{
			"is_verified":    true,
			"verified_until": bson.M{"$gt": at},
		}}
	case "unverified":
		filter["$and"] = []bson.M{{"$or": []bson.M{
			{"is_verified": bson.M{"$ne": true}},
			{"verified_until": bson.M{"$exists": false}},
			{"verified_until": nil},
			{"verified_until": bson.M{"$lte": at}},
		}}}
	}
	return filter
}

func HandleListArtisans(c echo.Context) error {
	page, limit := parsePageLimit(c)
	now := time.Now().UTC()
	verification := c.QueryParam("verification")
	if verification != "" && verification != "verified" && verification != "unverified" {
		return responses.SendError(c, http.StatusBadRequest, "invalid verification filter")
	}

	filter := buildArtisanFilter(c.QueryParam("status"), c.QueryParam("search"), verification, now)
	artisanUsers, total, err := users.ListAllUsers(filter, page, limit)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list artisans")
	}

	items := make([]artisanListItem, 0, len(artisanUsers))
	for _, artisan := range artisanUsers {
		plans, err := effectiveArtisanPlans(c.Request().Context(), artisan.ID, now)
		if err != nil {
			return responses.SendError(c, http.StatusInternalServerError, "failed to load artisan subscriptions")
		}
		items = append(items, artisanListItem{
			User:               artisan,
			SubscriptionPlans:  plans,
			VerificationActive: verificationActive(artisan, now),
		})
	}

	return responses.SendOK(c, Page[artisanListItem]{
		Data:  items,
		Total: total,
		Page:  page,
		Limit: limit,
	}, "Artisans retrieved")
}

func loadArtisanCV(ctx context.Context, userID string) (*files.FileResponse, error) {
	if artisanFileRepository == nil || artisanFileStorage == nil {
		return nil, nil
	}
	file, err := artisanFileRepository.FindByUserAndDocType(ctx, userID, files.CVDocumentTypeID)
	if errors.Is(err, files.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	objectKey, err := artisanFileStorage.KeyFromURL(file.FileURL)
	if err != nil || !strings.HasPrefix(objectKey, "users/"+userID+"/") {
		return nil, errors.New("stored CV URL is invalid")
	}
	downloadURL, err := artisanFileStorage.PresignDownload(ctx, objectKey)
	if err != nil {
		return nil, err
	}
	return &files.FileResponse{File: *file, DownloadURL: downloadURL}, nil
}

func HandleGetArtisan(c echo.Context) error {
	id := c.Param("id")
	profile, err := profiles.GetFullProfile(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return responses.SendError(c, http.StatusNotFound, "artisan not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load artisan")
	}
	if profile.User == nil || profile.UserType != "artisan" {
		return responses.SendError(c, http.StatusNotFound, "artisan not found")
	}

	now := time.Now().UTC()
	plans, err := effectiveArtisanPlans(c.Request().Context(), id, now)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load artisan subscriptions")
	}
	history, err := artisanSubscriptionHistory(c.Request().Context(), id, now)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load subscription history")
	}
	cv, err := loadArtisanCV(c.Request().Context(), id)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to prepare artisan CV")
	}

	return responses.SendOK(c, artisanDetail{
		FullProfile:         profile,
		SubscriptionPlans:   plans,
		SubscriptionHistory: history,
		CV:                  cv,
		VerificationActive:  verificationActive(*profile.User, now),
	}, "Artisan retrieved")
}

type updateArtisanVerificationRequest struct {
	IsVerified    *bool      `json:"is_verified"`
	VerifiedUntil *time.Time `json:"verified_until"`
}

func HandleUpdateArtisanVerification(c echo.Context) error {
	id := c.Param("id")
	var req updateArtisanVerificationRequest
	if err := c.Bind(&req); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}
	if req.IsVerified == nil {
		return responses.SendError(c, http.StatusBadRequest, "is_verified is required")
	}

	artisan, err := users.GetUserByID(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return responses.SendError(c, http.StatusNotFound, "artisan not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load artisan")
	}
	if artisan.UserType != "artisan" {
		return responses.SendError(c, http.StatusNotFound, "artisan not found")
	}

	update := bson.M{"is_verified": *req.IsVerified}
	if *req.IsVerified {
		verifiedUntil := time.Now().UTC().Add(defaultVerificationDuration)
		if req.VerifiedUntil != nil {
			verifiedUntil = req.VerifiedUntil.UTC()
		}
		if !verifiedUntil.After(time.Now().UTC()) {
			return responses.SendError(c, http.StatusBadRequest, "verified_until must be in the future")
		}
		update["verified_until"] = verifiedUntil
	} else {
		update["verified_until"] = nil
	}

	if err := users.UpdateUser(id, update); err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to update artisan verification")
	}
	updated, err := users.GetUserByID(c.Request().Context(), id)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load updated artisan")
	}
	return responses.SendOK(c, updated, "Artisan verification updated")
}
