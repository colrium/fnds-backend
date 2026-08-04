package jobrequests

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/configs"
	"backend/contextutil"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestJobRequestHandlersUseCanceledRequestContextForInitialLookup(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		handle     echo.HandlerFunc
		wantStatus int
	}{
		{name: "create vacancy user lookup", path: "/vacancies", handle: HandleCreateJobVacancy, wantStatus: http.StatusForbidden},
		{name: "list available profile lookup", path: "/vacancies/available", handle: HandleListAvailableJobVacancies, wantStatus: http.StatusInternalServerError},
		{name: "create application user lookup", path: "/applications", handle: HandleCreateJobApplication, wantStatus: http.StatusForbidden},
		{name: "browse artisans user lookup", path: "/artisans", handle: HandleEmployerBrowseArtisans, wantStatus: http.StatusForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock).ShareClient(true).DatabaseName("test"))
			previous := configs.MI
			configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
			t.Cleanup(func() { configs.MI = previous })

			requestCtx, cancel := context.WithCancel(context.Background())
			cancel()
			request := httptest.NewRequest(http.MethodPost, test.path, nil).WithContext(requestCtx)
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(request, recorder)
			contextutil.SetUser(ctx, "user-1")

			if err := test.handle(ctx); err != nil {
				t.Fatalf("handler error = %v", err)
			}
			if recorder.Code != test.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}
