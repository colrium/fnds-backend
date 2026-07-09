package quotations

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

type Quotation struct {
	ID           string          `json:"id" bson:"_id"`
	JobRequestID string          `json:"job_request_id" bson:"job_request_id"`
	ArtisanID    string          `json:"artisan_id" bson:"artisan_id"`
	CustomerID   string          `json:"customer_id" bson:"customer_id"`
	Status       string          `json:"status" bson:"status"`
	TotalAmount  float64         `json:"total_amount" bson:"total_amount"`
	Notes        string          `json:"notes" bson:"notes"`
	Items        []QuotationItem `json:"items" bson:"items"`
	ValidUntil   string          `json:"valid_until" bson:"valid_until"`
	CreatedAt    time.Time       `json:"created_at" bson:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at" bson:"updated_at"`
}

type QuotationItem struct {
	ID          string  `json:"id" bson:"_id"`
	Description string  `json:"description" bson:"description"`
	Quantity    int     `json:"quantity" bson:"quantity"`
	UnitPrice   float64 `json:"unit_price" bson:"unit_price"`
	TotalPrice  float64 `json:"total_price" bson:"total_price"`
}

type ExternalQuotation struct {
	ID            string                  `json:"id" bson:"_id"`
	ArtisanID     string                  `json:"artisan_id" bson:"artisan_id"`
	CustomerName  string                  `json:"customer_name" bson:"customer_name"`
	CustomerEmail string                  `json:"customer_email" bson:"customer_email"`
	CustomerPhone string                  `json:"customer_phone" bson:"customer_phone"`
	Status        string                  `json:"status" bson:"status"`
	TotalAmount   float64                 `json:"total_amount" bson:"total_amount"`
	Items         []ExternalQuotationItem `json:"items" bson:"items"`
	CreatedAt     time.Time               `json:"created_at" bson:"created_at"`
	UpdatedAt     time.Time               `json:"updated_at" bson:"updated_at"`
}

type ExternalQuotationItem struct {
	ID          string  `json:"id" bson:"_id"`
	Description string  `json:"description" bson:"description"`
	Quantity    int     `json:"quantity" bson:"quantity"`
	UnitPrice   float64 `json:"unit_price" bson:"unit_price"`
	TotalPrice  float64 `json:"total_price" bson:"total_price"`
}

type ExternalInvoice struct {
	ID          string    `json:"id" bson:"_id"`
	QuotationID string    `json:"quotation_id" bson:"quotation_id"`
	ArtisanID   string    `json:"artisan_id" bson:"artisan_id"`
	Status      string    `json:"status" bson:"status"`
	TotalAmount float64   `json:"total_amount" bson:"total_amount"`
	DueDate     string    `json:"due_date" bson:"due_date"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" bson:"updated_at"`
}

type ExternalPaymentBill struct {
	ID          string    `json:"id" bson:"_id"`
	InvoiceID   string    `json:"invoice_id" bson:"invoice_id"`
	ArtisanID   string    `json:"artisan_id" bson:"artisan_id"`
	Status      string    `json:"status" bson:"status"`
	TotalAmount float64   `json:"total_amount" bson:"total_amount"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" bson:"updated_at"`
}

func CreateQuotation(q *Quotation) error {
	q.ID = uuid.New().String()
	q.CreatedAt = time.Now()
	q.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("quotations").InsertOne(context.TODO(), q)
	return err
}

func GetQuotationByID(id string) (*Quotation, error) {
	var q Quotation
	err := configs.MI.DB.Collection("quotations").FindOne(context.TODO(), bson.M{"_id": id}).Decode(&q)
	if err != nil {
		return nil, err
	}
	return &q, nil
}

func UpdateQuotation(id string, updates bson.M) error {
	updates["updated_at"] = time.Now()
	_, err := configs.MI.DB.Collection("quotations").UpdateOne(context.TODO(), bson.M{"_id": id}, bson.M{"$set": updates})
	return err
}

func ListQuotations(filter bson.M) ([]Quotation, error) {
	cursor, err := configs.MI.DB.Collection("quotations").Find(context.TODO(), filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var quotations []Quotation
	if err := cursor.All(context.TODO(), &quotations); err != nil {
		return nil, err
	}
	return quotations, nil
}

func CreateExternalQuotation(eq *ExternalQuotation) error {
	eq.ID = uuid.New().String()
	eq.CreatedAt = time.Now()
	eq.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("external_quotations").InsertOne(context.TODO(), eq)
	return err
}

func GetExternalQuotationByID(id string) (*ExternalQuotation, error) {
	var eq ExternalQuotation
	err := configs.MI.DB.Collection("external_quotations").FindOne(context.TODO(), bson.M{"_id": id}).Decode(&eq)
	if err != nil {
		return nil, err
	}
	return &eq, nil
}

func UpdateExternalQuotation(id string, updates bson.M) error {
	updates["updated_at"] = time.Now()
	_, err := configs.MI.DB.Collection("external_quotations").UpdateOne(context.TODO(), bson.M{"_id": id}, bson.M{"$set": updates})
	return err
}
