package transactions

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

type Transaction struct {
	ID           string    `json:"id" bson:"_id"`
	UserID       string    `json:"user_id" bson:"user_id"`
	JobRequestID string    `json:"job_request_id,omitempty" bson:"job_request_id,omitempty"`
	Amount       float64   `json:"amount" bson:"amount"`
	Status       string    `json:"status" bson:"status"`
	Type         string    `json:"type" bson:"type"`
	Provider     string    `json:"provider" bson:"provider"`
	Reference    string    `json:"reference" bson:"reference"`
	Description  string    `json:"description" bson:"description"`
	CreatedAt    time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" bson:"updated_at"`
}

type WalletTransaction struct {
	ID          string    `json:"id" bson:"_id"`
	UserID      string    `json:"user_id" bson:"user_id"`
	Amount      float64   `json:"amount" bson:"amount"`
	Type        string    `json:"type" bson:"type"`
	Description string    `json:"description" bson:"description"`
	Reference   string    `json:"reference" bson:"reference"`
	Balance     float64   `json:"balance" bson:"balance"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
}

type FlutterwaveTransaction struct {
	ID             string    `json:"id" bson:"_id"`
	TransactionRef string    `json:"transaction_ref" bson:"transaction_ref"`
	FlutterwaveRef string    `json:"flutterwave_ref" bson:"flutterwave_ref"`
	Amount         float64   `json:"amount" bson:"amount"`
	Currency       string    `json:"currency" bson:"currency"`
	Status         string    `json:"status" bson:"status"`
	UserID         string    `json:"user_id" bson:"user_id"`
	CreatedAt      time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" bson:"updated_at"`
}

type CellulantTransaction struct {
	ID            string    `json:"id" bson:"_id"`
	TransactionID string    `json:"transaction_id" bson:"transaction_id"`
	Amount        float64   `json:"amount" bson:"amount"`
	Status        string    `json:"status" bson:"status"`
	UserID        string    `json:"user_id" bson:"user_id"`
	CreatedAt     time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" bson:"updated_at"`
}

type KopoKopoTransaction struct {
	ID             string    `json:"id" bson:"_id"`
	TransactionRef string    `json:"transaction_ref" bson:"transaction_ref"`
	Amount         float64   `json:"amount" bson:"amount"`
	Status         string    `json:"status" bson:"status"`
	UserID         string    `json:"user_id" bson:"user_id"`
	CreatedAt      time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" bson:"updated_at"`
}

func CreateTransaction(t *Transaction) error {
	t.ID = uuid.New().String()
	t.CreatedAt = time.Now()
	t.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("transactions").InsertOne(context.TODO(), t)
	return err
}

func GetTransactionByID(id string) (*Transaction, error) {
	var t Transaction
	err := configs.MI.DB.Collection("transactions").FindOne(context.TODO(), bson.M{"_id": id}).Decode(&t)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func UpdateTransaction(id string, updates bson.M) error {
	updates["updated_at"] = time.Now()
	_, err := configs.MI.DB.Collection("transactions").UpdateOne(context.TODO(), bson.M{"_id": id}, bson.M{"$set": updates})
	return err
}

func CreateWalletTransaction(wt *WalletTransaction) error {
	wt.ID = uuid.New().String()
	wt.CreatedAt = time.Now()
	_, err := configs.MI.DB.Collection("wallet_transactions").InsertOne(context.TODO(), wt)
	return err
}

func CreateFlutterwaveTransaction(ft *FlutterwaveTransaction) error {
	ft.ID = uuid.New().String()
	ft.CreatedAt = time.Now()
	ft.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("flutterwave_transactions").InsertOne(context.TODO(), ft)
	return err
}

func UpdateFlutterwaveTransaction(ref string, updates bson.M) error {
	updates["updated_at"] = time.Now()
	_, err := configs.MI.DB.Collection("flutterwave_transactions").UpdateOne(context.TODO(), bson.M{"transaction_ref": ref}, bson.M{"$set": updates})
	return err
}

func CreateCellulantTransaction(ct *CellulantTransaction) error {
	ct.ID = uuid.New().String()
	ct.CreatedAt = time.Now()
	ct.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("cellulant_transactions").InsertOne(context.TODO(), ct)
	return err
}

func UpdateCellulantTransaction(id string, updates bson.M) error {
	updates["updated_at"] = time.Now()
	_, err := configs.MI.DB.Collection("cellulant_transactions").UpdateOne(
		context.TODO(),
		bson.M{"transaction_id": id},
		bson.M{"$set": updates},
	)
	return err
}

func CreateKopoKopoTransaction(kt *KopoKopoTransaction) error {
	kt.ID = uuid.New().String()
	kt.CreatedAt = time.Now()
	kt.UpdatedAt = time.Now()
	_, err := configs.MI.DB.Collection("kopokopo_transactions").InsertOne(context.TODO(), kt)
	return err
}

func UpdateKopoKopoTransaction(ref string, updates bson.M) error {
	updates["updated_at"] = time.Now()
	_, err := configs.MI.DB.Collection("kopokopo_transactions").UpdateOne(context.TODO(), bson.M{"transaction_ref": ref}, bson.M{"$set": updates})
	return err
}

func ListTransactions(filter bson.M) ([]Transaction, error) {
	cursor, err := configs.MI.DB.Collection("transactions").Find(context.TODO(), filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var txns []Transaction
	if err := cursor.All(context.TODO(), &txns); err != nil {
		return nil, err
	}
	return txns, nil
}
