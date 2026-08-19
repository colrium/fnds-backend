package workspaces

import (
	"context"
	"log/slog"
	"strings"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// IsStytchAuthEnabled reports whether Stytch employer auth is enabled.
func IsStytchAuthEnabled() bool {
	flag := strings.TrimSpace(configs.StytchEmployerAuthEnabled())
	return flag == "true" || flag == "1"
}

// IsLegacyAuthEnabled reports whether legacy employer auth is still enabled.
func IsLegacyAuthEnabled() bool {
	flag := strings.TrimSpace(configs.LegacyEmployerAuthEnabled())
	return flag == "true" || flag == "1"
}

// IsUserMigrated checks if a user has an Account_Link (i.e., has been migrated
// to Stytch). Used by the login handler to decide whether to serve the legacy
// login or redirect to Stytch.
//
// A database error (timeout, connection failure, network partition) is
// distinguished from "not found": treating any error as "not migrated" would
// route a migrated employer to the legacy login flow during a DB outage,
// which is the wrong failure mode for an auth decision — fail closed instead
// by reporting migrated=true, which sends the user to Stytch sign-in rather
// than a deprecated auth path.
func IsUserMigrated(ctx context.Context, userID string) bool {
	coll := configs.MI.DB.Collection("account_links")
	err := coll.FindOne(ctx, bson.M{"user_id": userID}).Err()
	if err == nil {
		return true
	}
	if err == mongo.ErrNoDocuments {
		return false
	}
	slog.ErrorContext(ctx, "IsUserMigrated: database error", "user_id", userID, "error", err)
	return true
}

// CutoverResponse is returned when legacy auth is disabled and a migrated
// employer tries to use POST /api/v1/login. (Req 21.11)
type CutoverResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	SignInURL string `json:"sign_in_url"`
}

// BuildCutoverResponse creates the 409 response body for a migrated employer
// trying to use the legacy login endpoint. (Req 21.11)
func BuildCutoverResponse() CutoverResponse {
	return CutoverResponse{
		Code:      "use_stytch_sign_in",
		Message:   "This account has been migrated to Stytch sign-in",
		SignInURL: "/employer/sign-up",
	}
}
