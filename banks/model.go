package banks

import (
	"context"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

type Bank struct {
	ID        string    `json:"id" bson:"_id"`
	Name      string    `json:"name" bson:"name"`
	Code      string    `json:"code" bson:"code"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
}

func GetAllBanks() ([]Bank, error) {
	cursor, err := configs.MI.DB.Collection("banks").Find(context.TODO(), bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())

	var banks []Bank
	if err := cursor.All(context.TODO(), &banks); err != nil {
		return nil, err
	}
	return banks, nil
}

func CreateBank(name, code string) error {
	_, err := configs.MI.DB.Collection("banks").InsertOne(context.TODO(), Bank{
		ID:        uuid.New().String(),
		Name:      name,
		Code:      code,
		CreatedAt: time.Now(),
	})
	return err
}

func GetBankByCode(code string) (*Bank, error) {
	var bank Bank
	err := configs.MI.DB.Collection("banks").FindOne(context.TODO(), bson.M{"code": code}).Decode(&bank)
	if err != nil {
		return nil, err
	}
	return &bank, nil
}

type BankAccount struct {
	ID        string    `json:"id" bson:"_id"`
	UserID    string    `json:"user_id" bson:"user_id"`
	BankID    string    `json:"bank_id" bson:"bank_id"`
	Name      string    `json:"name" bson:"name"`
	Number    string    `json:"number" bson:"number"`
	Active    bool      `json:"active" bson:"active"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
}

func CreateBankAccount(ba *BankAccount) error {
	ba.ID = uuid.New().String()
	ba.CreatedAt = time.Now()
	ba.UpdatedAt = time.Now()
	ba.Active = true
	_, err := configs.MI.DB.Collection("bank_accounts").InsertOne(context.TODO(), ba)
	return err
}

func UpsertBankAccount(ba *BankAccount) error {
	ba.UpdatedAt = time.Now()
	result := configs.MI.DB.Collection("bank_accounts").FindOneAndUpdate(
		context.TODO(),
		bson.M{"number": ba.Number, "user_id": ba.UserID},
		bson.M{"$set": bson.M{
			"bank_id":    ba.BankID,
			"name":       ba.Name,
			"updated_at": ba.UpdatedAt,
		}},
	)
	if result.Err() != nil {
		return CreateBankAccount(ba)
	}
	return nil
}

func ListBankAccountsByUser(userID string) ([]BankAccount, error) {
	cursor, err := configs.MI.DB.Collection("bank_accounts").Find(context.TODO(), bson.M{"user_id": userID})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.TODO())
	var accounts []BankAccount
	if err := cursor.All(context.TODO(), &accounts); err != nil {
		return nil, err
	}
	return accounts, nil
}
