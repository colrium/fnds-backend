package notifications

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"backend/configs"
	"backend/integrations"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// AdminUserNames resolves only the display fields needed by Slack. Callers use
// readable fallbacks when a record is missing or MongoDB is unavailable.
func AdminUserNames(ctx context.Context, ids ...string) map[string]string {
	names := make(map[string]string)
	if configs.MI.DB == nil {
		return names
	}
	var unique []string
	seen := make(map[string]bool)
	for _, id := range ids {
		if id != "" && !seen[id] {
			unique = append(unique, id)
			seen[id] = true
		}
	}
	if len(unique) == 0 {
		return names
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cursor, err := configs.MI.DB.Collection("users").Find(ctx,
		bson.M{"_id": bson.M{"$in": unique}},
		options.Find().SetProjection(bson.M{"first_name": 1, "last_name": 1, "email": 1}),
	)
	if err != nil {
		slog.Warn("slack user name lookup failed", "count", len(unique))
		return names
	}
	defer func() { _ = cursor.Close(ctx) }()
	var records []struct {
		ID        string `bson:"_id"`
		FirstName string `bson:"first_name"`
		LastName  string `bson:"last_name"`
		Email     string `bson:"email"`
	}
	if err := cursor.All(ctx, &records); err != nil {
		slog.Warn("slack user name decode failed", "count", len(unique))
		return names
	}
	for _, record := range records {
		names[record.ID] = integrations.DisplayName(record.FirstName+" "+record.LastName, record.Email, "User account")
	}
	return names
}

func AdminSkillName(ctx context.Context, id string) string {
	if id == "" || configs.MI.DB == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var skill struct {
		Name string `bson:"name"`
	}
	if err := configs.MI.DB.Collection("skills").FindOne(ctx, bson.M{"_id": id},
		options.FindOne().SetProjection(bson.M{"name": 1})).Decode(&skill); err != nil {
		return ""
	}
	return strings.TrimSpace(skill.Name)
}
