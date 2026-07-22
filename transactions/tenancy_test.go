package transactions

import (
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestUserTransactionFilterScopesToOwner(t *testing.T) {
	t.Parallel()

	got := userTransactionFilter("transaction-1", "user-1")
	want := bson.M{"_id": "transaction-1", "user_id": "user-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("userTransactionFilter() = %#v, want %#v", got, want)
	}
}
