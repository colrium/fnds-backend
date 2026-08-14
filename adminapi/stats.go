package adminapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"backend/configs"
	"backend/responses"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

const (
	defaultStatsDays = 30
	maxStatsDays     = 365
)

// DailyCount is one point in a time series returned to the dashboard charts.
type DailyCount struct {
	Date  string `json:"date"`
	Count int64  `json:"count"`
}

// Stats is the envelope for the admin dashboard overview: breakdown counts
// plus a daily signup series for the requested window.
type Stats struct {
	UsersByType          map[string]int64 `json:"users_by_type"`
	UsersByStatus        map[string]int64 `json:"users_by_status"`
	JobRequestsByStatus  map[string]int64 `json:"job_requests_by_status"`
	TransactionsByStatus map[string]int64 `json:"transactions_by_status"`
	TransactionVolume    float64          `json:"transaction_volume"`
	SignupSeries         []DailyCount     `json:"signup_series"`
	Days                 int              `json:"days"`
}

func parseStatsDays(c echo.Context) int {
	days := defaultStatsDays
	if raw := c.QueryParam("days"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			days = v
		}
	}
	if days > maxStatsDays {
		days = maxStatsDays
	}
	return days
}

func HandleGetStats(c echo.Context) error {
	ctx := c.Request().Context()
	days := parseStatsDays(c)

	usersByType, err := countGroupedBy(ctx, "users", "user_type")
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load user stats")
	}
	usersByStatus, err := countGroupedBy(ctx, "users", "status")
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load user stats")
	}
	jobRequestsByStatus, err := countGroupedBy(ctx, "job_requests", "status")
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load job request stats")
	}
	transactionsByStatus, err := countGroupedBy(ctx, "transactions", "status")
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load transaction stats")
	}
	transactionVolume, err := sumField(ctx, "transactions", "amount")
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load transaction stats")
	}

	end := time.Now().UTC().Truncate(24 * time.Hour)
	start := end.AddDate(0, 0, -(days - 1))
	signupCounts, err := countByDay(ctx, "users", "created_at", start)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to load signup stats")
	}

	stats := Stats{
		UsersByType:          usersByType,
		UsersByStatus:        usersByStatus,
		JobRequestsByStatus:  jobRequestsByStatus,
		TransactionsByStatus: transactionsByStatus,
		TransactionVolume:    transactionVolume,
		SignupSeries:         buildDailySeries(signupCounts, start, days),
		Days:                 days,
	}
	return responses.SendOK(c, stats, "Stats retrieved")
}

// countGroupedBy runs a $group count over field in collection, returning an
// empty (never nil) map when there is no data.
func countGroupedBy(ctx context.Context, collection, field string) (map[string]int64, error) {
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$group", Value: bson.M{
			"_id":   "$" + field,
			"count": bson.M{"$sum": 1},
		}}},
	}
	cursor, err := configs.MI.DB.Collection(collection).Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	result := map[string]int64{}
	var rows []struct {
		ID    string `bson:"_id"`
		Count int64  `bson:"count"`
	}
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.ID == "" {
			continue
		}
		result[row.ID] = row.Count
	}
	return result, nil
}

// sumField sums a numeric field across every document in collection.
func sumField(ctx context.Context, collection, field string) (float64, error) {
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$group", Value: bson.M{
			"_id":   nil,
			"total": bson.M{"$sum": "$" + field},
		}}},
	}
	cursor, err := configs.MI.DB.Collection(collection).Aggregate(ctx, pipeline)
	if err != nil {
		return 0, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	var row struct {
		Total float64 `bson:"total"`
	}
	if cursor.Next(ctx) {
		if err := cursor.Decode(&row); err != nil {
			return 0, err
		}
	}
	return row.Total, nil
}

// countByDay groups documents in collection by the UTC calendar day of
// dateField, for documents on or after start. Returns a map keyed by
// "YYYY-MM-DD" with no entry for days that had zero documents.
func countByDay(ctx context.Context, collection, dateField string, start time.Time) (map[string]int64, error) {
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.M{
			dateField: bson.M{"$gte": start},
		}}},
		bson.D{{Key: "$group", Value: bson.M{
			"_id": bson.M{"$dateToString": bson.M{
				"format": "%Y-%m-%d",
				"date":   "$" + dateField,
			}},
			"count": bson.M{"$sum": 1},
		}}},
	}
	cursor, err := configs.MI.DB.Collection(collection).Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()

	result := map[string]int64{}
	var rows []struct {
		ID    string `bson:"_id"`
		Count int64  `bson:"count"`
	}
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ID] = row.Count
	}
	return result, nil
}

// buildDailySeries fills every day from start for `days` days, defaulting
// missing days to zero so the frontend chart never sees a gap.
func buildDailySeries(counts map[string]int64, start time.Time, days int) []DailyCount {
	series := make([]DailyCount, 0, days)
	for i := 0; i < days; i++ {
		day := start.AddDate(0, 0, i)
		key := day.Format("2006-01-02")
		series = append(series, DailyCount{Date: key, Count: counts[key]})
	}
	return series
}
