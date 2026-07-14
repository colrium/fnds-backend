package integrations

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"backend/configs"
)

const advantaSendPath = "/api/services/sendsms/"

func advantaBaseURL() string {
	if v := os.Getenv("ADVANTA_BASE_URL"); v != "" {
		return v
	}
	return "https://quicksms.advantasms.com"
}

type advantaRequest struct {
	ApiKey    string `json:"apikey"`
	PartnerID string `json:"partnerID"`
	Message   string `json:"message"`
	Shortcode string `json:"shortcode"`
	Mobile    string `json:"mobile"`
}

// advantaResponse mirrors Advanta's envelope: {"responses":[{"response-code":200,...}]}.
type advantaResponse struct {
	Responses []struct {
		ResponseCode        int    `json:"response-code"`
		ResponseDescription string `json:"response-description"`
		Mobile              string `json:"mobile"`
		MessageID           int64  `json:"messageid"`
	} `json:"responses"`
}

// SendAdvantaSMS delivers a single SMS through Advanta's Bulk SMS API.
// See https://advanta.africa/bulk-sms-api.
func SendAdvantaSMS(mobile, message string) error {
	apiKey := configs.AdvantaApiKey()
	partnerID := configs.AdvantaPartnerID()
	shortcode := configs.AdvantaShortcode()
	if apiKey == "" || partnerID == "" || shortcode == "" {
		return fmt.Errorf("advanta sms not configured (set ADVANTA_API_KEY, ADVANTA_PARTNER_ID, ADVANTA_SHORTCODE)")
	}

	payload := advantaRequest{
		ApiKey:    apiKey,
		PartnerID: partnerID,
		Message:   message,
		Shortcode: shortcode,
		Mobile:    mobile,
	}

	body := new(bytes.Buffer)
	if err := json.NewEncoder(body).Encode(payload); err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, advantaBaseURL()+advantaSendPath, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	var parsed advantaResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("advanta sms: decode response (http %d): %w", res.StatusCode, err)
	}

	if len(parsed.Responses) == 0 {
		return fmt.Errorf("advanta sms: empty response (http %d)", res.StatusCode)
	}

	first := parsed.Responses[0]
	if first.ResponseCode != 200 {
		return fmt.Errorf("advanta sms: send failed (code %d: %s)", first.ResponseCode, first.ResponseDescription)
	}
	return nil
}
