package workspaces

import (
	"context"
	"time"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Invoice represents an employer billing invoice. This is a NEW collection —
// external_invoices is the artisan→customer invoice keyed by artisan_id and is
// NOT repurposed. (Design: "The invoices collection does not exist yet")
type Invoice struct {
	ID             string     `json:"id" bson:"_id"`
	WorkspaceID    string     `json:"workspace_id" bson:"workspace_id"`
	WorkspaceName  string     `json:"workspace_name" bson:"workspace_name"` // Req 24.9, snapshot at issue
	Number         string     `json:"number" bson:"number"`
	SubscriptionID string     `json:"subscription_id,omitempty" bson:"subscription_id,omitempty"`
	Status         string     `json:"status" bson:"status"` // open | paid | void
	AmountKES      int64      `json:"amount" bson:"amount"`
	Currency       string     `json:"currency" bson:"currency"`
	IssuedAt       time.Time  `json:"issued_at" bson:"issued_at"`
	DueAt          *time.Time `json:"due_at,omitempty" bson:"due_at,omitempty"`
	PaidAt         *time.Time `json:"paid_at,omitempty" bson:"paid_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at" bson:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at" bson:"updated_at"`
}

// EnsureInvoiceIndexes creates the indexes for the invoices collection.
// Called once at startup from main.go. Idempotent.
// Requirements: 9.1, 18.9, 24.1, 24.8, 24.9.
func EnsureInvoiceIndexes(ctx context.Context) error {
	coll := configs.MI.DB.Collection("invoices")

	_, err := coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			// uniq_invoice_number — {number: 1} unique
			Keys:    bson.D{{Key: "number", Value: 1}},
			Options: options.Index().SetName("uniq_invoice_number").SetUnique(true),
		},
		{
			// invoices_workspace_issued — {workspace_id: 1, issued_at: -1} non-unique
			Keys:    bson.D{{Key: "workspace_id", Value: 1}, {Key: "issued_at", Value: -1}},
			Options: options.Index().SetName("invoices_workspace_issued"),
		},
	})
	return err
}
