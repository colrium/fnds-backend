package payments

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

type FlutterwaveConfig struct {
	SecretKey string
	BaseURL   string
}

type FlutterwavePaymentRequest struct {
	TxRef          string              `json:"tx_ref"`
	Amount         float64             `json:"amount"`
	Currency       string              `json:"currency"`
	RedirectURL    string              `json:"redirect_url"`
	PaymentOptions string              `json:"payment_options,omitempty"`
	Customer       FlutterwaveCustomer `json:"customer"`
}

type FlutterwaveCustomer struct {
	Email       string `json:"email"`
	PhoneNumber string `json:"phonenumber"`
	Name        string `json:"name"`
}

type FlutterwaveResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Data    struct {
		Link          string `json:"link"`
		TxRef         string `json:"tx_ref"`
		FlwRef        string `json:"flw_ref"`
		TransactionID int    `json:"id"`
	} `json:"data"`
}

func NewFlutterwaveService(secretKey string) *FlutterwaveConfig {
	return &FlutterwaveConfig{
		SecretKey: secretKey,
		BaseURL:   "https://api.flutterwave.com/v3",
	}
}

func (f *FlutterwaveConfig) CreatePayment(req FlutterwavePaymentRequest) (*FlutterwaveResponse, error) {
	body := new(bytes.Buffer)
	if err := json.NewEncoder(body).Encode(req); err != nil {
		return nil, fmt.Errorf("failed to encode Flutterwave payment request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", f.BaseURL+"/payments", body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+f.SecretKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result FlutterwaveResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

func (f *FlutterwaveConfig) VerifyTransaction(transactionID int) (*FlutterwaveResponse, error) {
	url := fmt.Sprintf("%s/transactions/%d/verify", f.BaseURL, transactionID)
	httpReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+f.SecretKey)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result FlutterwaveResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}
