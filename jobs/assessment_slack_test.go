package jobs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/configs"
	"backend/integrations"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestNoShowAlertLinksRecordsAndMarksOnlySuccessfulDelivery(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, status := range []int{http.StatusOK, http.StatusBadGateway} {
		mt.Run(http.StatusText(status), func(mt *mtest.T) {
			previous := configs.MI
			configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
			mt.Cleanup(func() { configs.MI = previous })
			mt.AddMockResponses(
				mtest.CreateCursorResponse(0, "test.assessment_appointments", mtest.FirstBatch,
					bson.D{{Key: "_id", Value: "appt1"}, {Key: "journey_id", Value: "j1"},
						{Key: "user_id", Value: "u1"}, {Key: "assessor_id", Value: "a1"},
						{Key: "stage", Value: "oral"}, {Key: "scheduled_for", Value: time.Now().Add(-3 * time.Hour)}}),
				mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch,
					bson.D{{Key: "_id", Value: "u1"}, {Key: "first_name", Value: "Jane"}},
					bson.D{{Key: "_id", Value: "a1"}, {Key: "first_name", Value: "Peter"}}),
				mtest.CreateSuccessResponse(),
			)
			payload := make(chan integrations.BlockKitMessage, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var msg integrations.BlockKitMessage
				_ = json.NewDecoder(r.Body).Decode(&msg)
				payload <- msg
				w.WriteHeader(status)
			}))
			defer server.Close()
			mt.Setenv("SLACK_OPS_NOTIFS_URL", server.URL)
			mt.Setenv("ADMIN_DASHBOARD_URL", "https://admin.fundis.co.ke")
			CheckNoShows()
			msg := <-payload
			for _, want := range []string{"/users/u1|Jane>", "/users/a1|Peter>", "/assessments/journeys/j1|Oral assessment>", "/assessments|Open assessments →>"} {
				if !strings.Contains(msg.Text, want) {
					mt.Errorf("missing %s: %s", want, msg.Text)
				}
			}
			updates := 0
			for _, event := range mt.GetAllStartedEvents() {
				if event.CommandName == "update" {
					updates++
				}
			}
			if (status == http.StatusOK && updates != 1) || (status != http.StatusOK && updates != 0) {
				mt.Errorf("delivery status %d produced %d alerted updates", status, updates)
			}
		})
	}
}
