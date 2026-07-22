package quotations

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"backend/contextutil"

	"github.com/labstack/echo/v4"
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

func TestHandleUpdateQuotationRejectsProtectedFieldOnlyPayload(t *testing.T) {
	t.Parallel()

	app := echo.New()
	request := httptest.NewRequest(
		http.MethodPatch,
		"/quotations/quotation-1",
		strings.NewReader(`{"customer_id":"attacker","total_amount":1}`),
	)
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	ctx := app.NewContext(request, recorder)
	ctx.SetParamNames("id")
	ctx.SetParamValues("quotation-1")
	contextutil.SetUser(ctx, "user-1")

	if err := HandleUpdateQuotation(ctx); err != nil {
		t.Fatalf("HandleUpdateQuotation() error = %v", err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("HandleUpdateQuotation() status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}
