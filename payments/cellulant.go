package payments

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
)

type CellulantConfig struct {
	ConsumerKey    string
	ConsumerSecret string
	BaseURL        string
}

type CellulantPaymentRequest struct {
	TransactionID string  `json:"transaction_id"`
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency"`
	CustomerPhone string  `json:"customer_phone"`
	CustomerEmail string  `json:"customer_email"`
	Narration     string  `json:"narration"`
	CallbackURL   string  `json:"callback_url"`
}

type CellulantResponse struct {
	StatusCode    int    `json:"status_code"`
	StatusDescription string `json:"status_description"`
	TransactionID string `json:"transaction_id"`
}

func NewCellulantService(consumerKey, consumerSecret string) *CellulantConfig {
	return &CellulantConfig{
		ConsumerKey:    consumerKey,
		ConsumerSecret: consumerSecret,
		BaseURL:        "https://api.cellulant.com/v1",
	}
}

func (c *CellulantConfig) GenerateSignature(payload string) string {
	mac := hmac.New(sha256.New, []byte(c.ConsumerSecret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *CellulantConfig) SendPaymentRequest(req CellulantPaymentRequest) (*CellulantResponse, error) {
	body := new(bytes.Buffer)
	json.NewEncoder(body).Encode(req)

	signature := c.GenerateSignature(body.String())

	httpReq, err := http.NewRequest("POST", c.BaseURL+"/payments", body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Consumer-Key", c.ConsumerKey)
	httpReq.Header.Set("X-Signature", signature)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	var result CellulantResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}
