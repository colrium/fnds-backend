package quotations

import (
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestRelatedQuotationFilterScopesBothParticipants(t *testing.T) {
	t.Parallel()

	got := relatedQuotationFilter("user-1")
	want := bson.M{"$or": bson.A{
		bson.M{"customer_id": "user-1"},
		bson.M{"artisan_id": "user-1"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("relatedQuotationFilter() = %#v, want %#v", got, want)
	}
}
