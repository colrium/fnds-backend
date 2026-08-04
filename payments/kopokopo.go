package payments

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

type KopoKopoConfig struct {
	APIKey  string
	BaseURL string
}

type KopoKopoPaymentRequest struct {
	Amount          float64           `json:"amount"`
	Currency        string            `json:"currency"`
	Destination     string            `json:"destination"`
	DestinationType string            `json:"destination_type"`
	CallbackURL     string            `json:"callback_url"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type KopoKopoResponse struct {
	Status         string `json:"status"`
	TransactionRef string `json:"transaction_ref"`
	Description    string `json:"description"`
}

func NewKopoKopoService(apiKey string) *KopoKopoConfig {
	return &KopoKopoConfig{
		APIKey:  apiKey,
		BaseURL: "https://api.kopokopo.com/api/v1",
	}
}

func (k *KopoKopoConfig) InitiatePayment(req KopoKopoPaymentRequest) (*KopoKopoResponse, error) {
	body := new(bytes.Buffer)
	if err := json.NewEncoder(body).Encode(req); err != nil {
		return nil, fmt.Errorf("failed to encode KopoKopo payment request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", k.BaseURL+"/payments", body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+k.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result KopoKopoResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

func (k *KopoKopoConfig) VerifyPayment(transactionRef string) (*KopoKopoResponse, error) {
	url := fmt.Sprintf("%s/payments/%s", k.BaseURL, transactionRef)
	httpReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+k.APIKey)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result KopoKopoResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}
