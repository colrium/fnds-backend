package identity

import (
	"context"
	"fmt"
	"time"

	"backend/audit"
	"backend/configs"
	"backend/users"

	"go.mongodb.org/mongo-driver/bson"
)

// ApplyEmailChange handles a Stytch-reported email address change for a
// linked Stytch Member (Req 5.13, 5.14, 19.6). memberID identifies the
// AccountLink by its stytch_member_id; newEmail is the raw address Stytch
// reported.
//
// If the normalized new address is unheld by any other users document, the
// linked document's email is updated and the AccountLink's linked_email is
// updated to match. If the address is already held by another users
// document, the previously stored email is retained, the AccountLink is
// flagged for manual review, and the collision is recorded without applying
// either side's data to the other.
//
// Either outcome writes exactly one Audit_Log_Entry naming both addresses in
// masked form.
func ApplyEmailChange(ctx context.Context, auditLogger *audit.Logger, memberID, newEmail string, now time.Time) error {
	linksColl := configs.MI.DB.Collection("account_links")

	var link AccountLink
	if err := linksColl.FindOne(ctx, bson.M{"stytch_member_id": memberID}).Decode(&link); err != nil {
		return fmt.Errorf("identity.ApplyEmailChange: find account_link for member %s: %w", memberID, err)
	}

	normalizedNew := NormalizeEmail(newEmail)
	previousEmail := link.LinkedEmail

	existing, err := users.GetUsersByNormalizedEmail(ctx, normalizedNew)
	if err != nil {
		return fmt.Errorf("identity.ApplyEmailChange: check email collision: %w", err)
	}

	// The linked user's own document does not count as a collision with itself.
	collides := false
	for _, u := range existing {
		if u.ID != link.UserID {
			collides = true
			break
		}
	}

	if collides {
		res, err := linksColl.UpdateOne(ctx,
			bson.M{"_id": link.ID},
			bson.M{"$set": bson.M{"review_required": true, "updated_at": now}},
		)
		if err != nil {
			return fmt.Errorf("identity.ApplyEmailChange: flag account_link for review: %w", err)
		}
		// A zero match means the link was deleted between the read and this
		// write; writing the audit entry then would claim a flag that does not
		// exist.
		if res.MatchedCount == 0 {
			return fmt.Errorf("identity.ApplyEmailChange: account_link %s not found for flagging", link.ID)
		}

		auditLogger.Write(ctx, audit.WriteInput{
			ActorUserID: link.UserID,
			ActorKind:   "system",
			Action:      audit.ActionAccountLinkEmailChanged,
			TargetKind:  "account_link",
			TargetID:    link.ID,
			Previous:    map[string]any{"email": MaskEmail(previousEmail)},
			New:         map[string]any{"email": MaskEmail(newEmail), "collision": true},
		})
		return nil
	}

	if err := users.UpdateUser(link.UserID, bson.M{"email": normalizedNew, "email_normalized": normalizedNew}); err != nil {
		return fmt.Errorf("identity.ApplyEmailChange: update users document: %w", err)
	}
	res, err := linksColl.UpdateOne(ctx,
		bson.M{"_id": link.ID},
		bson.M{"$set": bson.M{"linked_email": normalizedNew, "updated_at": now}},
	)
	if err != nil {
		return fmt.Errorf("identity.ApplyEmailChange: update account_link: %w", err)
	}
	// The users document already carries the new email; a zero match here means
	// linked_email is now stale, which the caller must know about.
	if res.MatchedCount == 0 {
		return fmt.Errorf("identity.ApplyEmailChange: account_link %s not found for email update", link.ID)
	}

	auditLogger.Write(ctx, audit.WriteInput{
		ActorUserID: link.UserID,
		ActorKind:   "system",
		Action:      audit.ActionAccountLinkEmailChanged,
		TargetKind:  "account_link",
		TargetID:    link.ID,
		Previous:    map[string]any{"email": MaskEmail(previousEmail)},
		New:         map[string]any{"email": MaskEmail(normalizedNew), "collision": false},
	})
	return nil
}
