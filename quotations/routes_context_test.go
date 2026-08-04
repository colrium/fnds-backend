package quotations

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"backend/contextutil"
	"backend/jobrequests"
	"backend/notifications"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
)

type quotationContextKey struct{}

const quotationContextValue = "request-context-value"

func TestHandleCreateQuotationPropagatesRequestContextAndDetachesNotification(t *testing.T) {
	t.Parallel()

	requestCtx, cancel := canceledQuotationRequestContext()
	defer cancel()
	ctx, recorder := newQuotationEchoContext(
		http.MethodPost,
		"/quotations",
		`{"job_request_id":"job-1","items":[{"description":"Labour","quantity":2,"unit_price":100}]}`,
		requestCtx,
	)
	contextutil.SetUser(ctx, "artisan-1")

	var lookupIDs []string
	deps := quotationRouteDependencies{
		getJobRequestByID: func(id string) (*jobrequests.JobRequest, error) {
			if id != "job-1" {
				t.Fatalf("getJobRequestByID() id = %q, want job-1", id)
			}
			return &jobrequests.JobRequest{
				ID:         id,
				ArtisanID:  "artisan-1",
				CustomerID: "customer-1",
				Title:      "Kitchen repair",
			}, nil
		},
		createQuotation: func(q *Quotation) error {
			q.ID = "quotation-1"
			return nil
		},
		getUserByID: func(ctx context.Context, id string) (*users.User, error) {
			assertCanceledRequestContext(t, ctx)
			lookupIDs = append(lookupIDs, id)
			if id == "customer-1" {
				return &users.User{ID: id, Email: "customer@example.com"}, nil
			}
			return &users.User{ID: id, FirstName: "Amina", LastName: "Fundi"}, nil
		},
		notifyQuotationReceived: func(ctx context.Context, event notifications.QuotationReceivedEvent) error {
			assertDetachedNotificationContext(t, ctx)
			if event.QuotationID != "quotation-1" {
				t.Fatalf("NotifyQuotationReceived() quotation ID = %q, want quotation-1", event.QuotationID)
			}
			return nil
		},
	}

	if err := handleCreateQuotation(ctx, deps); err != nil {
		t.Fatalf("handleCreateQuotation() error = %v", err)
	}
	if recorder.Code != http.StatusCreated {
		t.Fatalf("handleCreateQuotation() status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	if want := []string{"customer-1", "artisan-1"}; !reflect.DeepEqual(lookupIDs, want) {
		t.Fatalf("user lookup IDs = %v, want %v", lookupIDs, want)
	}
}

func TestHandleUpdateQuotationPropagatesRequestContextAndDetachesNotification(t *testing.T) {
	t.Parallel()

	requestCtx, cancel := canceledQuotationRequestContext()
	defer cancel()
	ctx, recorder := newQuotationEchoContext(
		http.MethodPatch,
		"/quotations/quotation-1",
		`{"status":"rejected"}`,
		requestCtx,
	)
	ctx.SetParamNames("id")
	ctx.SetParamValues("quotation-1")
	contextutil.SetUser(ctx, "customer-1")

	var lookupIDs []string
	deps := quotationRouteDependencies{
		getQuotationByID: func(id string) (*Quotation, error) {
			return &Quotation{
				ID:         id,
				CustomerID: "customer-1",
				ArtisanID:  "artisan-1",
				Status:     "pending",
			}, nil
		},
		updateQuotationForUser: func(id, userID string, updates bson.M) (bool, error) {
			if id != "quotation-1" || userID != "customer-1" || updates["status"] != "rejected" {
				t.Fatalf("UpdateQuotationForUser(%q, %q, %v) received unexpected arguments", id, userID, updates)
			}
			return true, nil
		},
		getUserByID: func(ctx context.Context, id string) (*users.User, error) {
			assertCanceledRequestContext(t, ctx)
			lookupIDs = append(lookupIDs, id)
			if id == "customer-1" {
				return &users.User{ID: id, FirstName: "Kamau", LastName: "Customer"}, nil
			}
			return &users.User{ID: id, Email: "artisan@example.com"}, nil
		},
		notifyQuotationDecision: func(ctx context.Context, event notifications.QuotationDecisionEvent) error {
			assertDetachedNotificationContext(t, ctx)
			if event.Status != "rejected" {
				t.Fatalf("NotifyQuotationDecision() status = %q, want rejected", event.Status)
			}
			return nil
		},
	}

	if err := handleUpdateQuotation(ctx, deps); err != nil {
		t.Fatalf("handleUpdateQuotation() error = %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("handleUpdateQuotation() status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if want := []string{"customer-1", "artisan-1"}; !reflect.DeepEqual(lookupIDs, want) {
		t.Fatalf("user lookup IDs = %v, want %v", lookupIDs, want)
	}
}

func TestHandleGetQuotationInvoicePropagatesRequestContextToCustomerLookup(t *testing.T) {
	t.Parallel()

	requestCtx, cancel := canceledQuotationRequestContext()
	defer cancel()
	ctx, recorder := newQuotationEchoContext(http.MethodGet, "/quotations/quotation-1/invoice", "", requestCtx)
	ctx.SetParamNames("id")
	ctx.SetParamValues("quotation-1")
	contextutil.SetUser(ctx, "customer-1")

	lookupCalled := false
	customerNotified := false
	invoice := &ExternalInvoice{ID: "invoice-1", QuotationID: "quotation-1", Status: InvoiceStatusUnpaid}
	deps := quotationRouteDependencies{
		getQuotationForUser: func(id, userID string) (*Quotation, error) {
			return &Quotation{
				ID:          id,
				CustomerID:  userID,
				ArtisanID:   "artisan-1",
				Status:      "accepted",
				TotalAmount: 250,
			}, nil
		},
		getExternalInvoiceByQuotationID: func(id string) (*ExternalInvoice, error) {
			return invoice, nil
		},
		getUserByID: func(ctx context.Context, id string) (*users.User, error) {
			assertCanceledRequestContext(t, ctx)
			lookupCalled = true
			return &users.User{
				ID:        id,
				FirstName: "Kamau",
				LastName:  "Customer",
				Email:     "customer@example.com",
				Phone:     "+254700000000",
			}, nil
		},
		createInvoicePaymentLink: func(gotInvoice *ExternalInvoice, eq *ExternalQuotation) (string, error) {
			if gotInvoice != invoice || eq.ID != "quotation-1" {
				t.Fatalf("createInvoicePaymentLink() received unexpected invoice or quotation")
			}
			return "https://pay.example/invoice-1", nil
		},
		notifyCustomerInvoice: func(eq *ExternalQuotation, link string) {
			customerNotified = true
		},
	}

	if err := handleGetQuotationInvoice(ctx, deps); err != nil {
		t.Fatalf("handleGetQuotationInvoice() error = %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("handleGetQuotationInvoice() status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !lookupCalled {
		t.Fatal("customer lookup was not called")
	}
	if !customerNotified {
		t.Fatal("customer invoice notification was not called")
	}
}

func TestInternalQuotationLookupFailuresSuppressNotificationDelivery(t *testing.T) {
	t.Parallel()

	errLookup := errors.New("lookup failed")
	q := &Quotation{
		ID:           "quotation-1",
		JobRequestID: "job-1",
		CustomerID:   "customer-1",
		ArtisanID:    "artisan-1",
	}
	job := &jobrequests.JobRequest{ID: "job-1", Title: "Kitchen repair"}
	tests := []struct {
		name        string
		failedID    string
		wantLookups int
		invoke      func(context.Context, quotationRouteDependencies)
	}{
		{
			name:        "created customer lookup",
			failedID:    "customer-1",
			wantLookups: 1,
			invoke: func(ctx context.Context, deps quotationRouteDependencies) {
				notifyInternalQuotationCreatedWithDependencies(ctx, q, job, deps)
			},
		},
		{
			name:        "created artisan lookup",
			failedID:    "artisan-1",
			wantLookups: 2,
			invoke: func(ctx context.Context, deps quotationRouteDependencies) {
				notifyInternalQuotationCreatedWithDependencies(ctx, q, job, deps)
			},
		},
		{
			name:        "decision customer lookup",
			failedID:    "customer-1",
			wantLookups: 1,
			invoke: func(ctx context.Context, deps quotationRouteDependencies) {
				notifyInternalQuotationDecisionWithDependencies(ctx, q, "accepted", deps)
			},
		},
		{
			name:        "decision artisan lookup",
			failedID:    "artisan-1",
			wantLookups: 2,
			invoke: func(ctx context.Context, deps quotationRouteDependencies) {
				notifyInternalQuotationDecisionWithDependencies(ctx, q, "accepted", deps)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lookups := 0
			deliveries := 0
			deps := quotationRouteDependencies{
				getUserByID: func(ctx context.Context, id string) (*users.User, error) {
					lookups++
					if id == tt.failedID {
						return nil, errLookup
					}
					return &users.User{ID: id}, nil
				},
				notifyQuotationReceived: func(context.Context, notifications.QuotationReceivedEvent) error {
					deliveries++
					return nil
				},
				notifyQuotationDecision: func(context.Context, notifications.QuotationDecisionEvent) error {
					deliveries++
					return nil
				},
			}

			tt.invoke(context.Background(), deps)

			if lookups != tt.wantLookups {
				t.Fatalf("lookup calls = %d, want %d", lookups, tt.wantLookups)
			}
			if deliveries != 0 {
				t.Fatalf("notification deliveries = %d, want 0", deliveries)
			}
		})
	}
}

func TestEnsureInternalQuotationInvoicePropagatesCallerCancellationToCustomerLookup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), quotationContextKey{}, quotationContextValue))
	cancel()
	invoice := &ExternalInvoice{ID: "invoice-1", QuotationID: "quotation-1", Status: InvoiceStatusUnpaid}
	paymentLinkCalled := false
	deps := quotationRouteDependencies{
		getExternalInvoiceByQuotationID: func(string) (*ExternalInvoice, error) {
			return invoice, nil
		},
		getUserByID: func(got context.Context, id string) (*users.User, error) {
			if got.Value(quotationContextKey{}) != quotationContextValue {
				t.Fatalf("customer lookup lost caller context value")
			}
			if !errors.Is(got.Err(), context.Canceled) {
				t.Fatalf("customer lookup context error = %v, want context.Canceled", got.Err())
			}
			return nil, got.Err()
		},
		createInvoicePaymentLink: func(*ExternalInvoice, *ExternalQuotation) (string, error) {
			paymentLinkCalled = true
			return "", nil
		},
	}

	gotInvoice, link, err := ensureInternalQuotationInvoiceWithDependencies(ctx, &Quotation{
		ID:         "quotation-1",
		CustomerID: "customer-1",
		ArtisanID:  "artisan-1",
	}, deps)

	if gotInvoice != invoice {
		t.Fatalf("ensureInternalQuotationInvoiceWithDependencies() invoice = %#v, want original invoice", gotInvoice)
	}
	if link != "" {
		t.Fatalf("ensureInternalQuotationInvoiceWithDependencies() link = %q, want empty", link)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ensureInternalQuotationInvoiceWithDependencies() error = %v, want context.Canceled", err)
	}
	if paymentLinkCalled {
		t.Fatal("payment link creation ran after customer lookup cancellation")
	}
}

func canceledQuotationRequestContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithValue(
		context.Background(),
		quotationContextKey{},
		quotationContextValue,
	))
	cancel()
	return ctx, cancel
}

func newQuotationEchoContext(
	method string,
	target string,
	body string,
	requestCtx context.Context,
) (echo.Context, *httptest.ResponseRecorder) {
	app := echo.New()
	request := httptest.NewRequest(method, target, strings.NewReader(body)).WithContext(requestCtx)
	if body != "" {
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	recorder := httptest.NewRecorder()
	return app.NewContext(request, recorder), recorder
}

func assertCanceledRequestContext(t *testing.T, ctx context.Context) {
	t.Helper()
	if ctx.Value(quotationContextKey{}) != quotationContextValue {
		t.Fatal("user lookup did not receive request context value")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("user lookup context error = %v, want context.Canceled", ctx.Err())
	}
}

func assertDetachedNotificationContext(t *testing.T, ctx context.Context) {
	t.Helper()
	if ctx.Value(quotationContextKey{}) != quotationContextValue {
		t.Fatal("notification context did not preserve request context value")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("notification context inherited request cancellation: %v", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("notification context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining < 4*time.Second || remaining > internalQuotationNotificationTimeout {
		t.Fatalf("notification deadline remaining = %v, want approximately %v", remaining, internalQuotationNotificationTimeout)
	}
}
