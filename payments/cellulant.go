package payments

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
)

type CellulantConfig struct {
	AccessKey   string
	IVKey       string
	SecretKey   string
	ServiceCode string
	CheckoutURL string
}

type CellulantService struct {
	config CellulantConfig
}

type CellulantCheckoutRequest struct {
	MSISDN                string  `json:"msisdn,omitempty"`
	LanguageCode          string  `json:"language_code"`
	RaiseInvoice          bool    `json:"raise_invoice"`
	RequestAmount         float64 `json:"request_amount"`
	RequestDescription    string  `json:"request_description,omitempty"`
	CallbackURL           string  `json:"callback_url"`
	FailRedirectURL       string  `json:"fail_redirect_url"`
	PendingRedirectURL    string  `json:"pending_redirect_url"`
	SuccessRedirectURL    string  `json:"success_redirect_url"`
	MerchantTransactionID string  `json:"merchant_transaction_id"`
	ServiceCode           string  `json:"service_code"`
	DueDate               string  `json:"due_date"`
	AccountNumber         string  `json:"account_number"`
	CurrencyCode          string  `json:"currency_code"`
	CountryCode           string  `json:"country_code"`
	CustomerFirstName     string  `json:"customer_first_name,omitempty"`
	CustomerLastName      string  `json:"customer_last_name,omitempty"`
	CustomerEmail         string  `json:"customer_email,omitempty"`
}

type CellulantPaymentEvent struct {
	CheckoutRequestID     string
	MerchantTransactionID string
	AccountNumber         string
	RequestStatusCode     int
	RequestStatusDescription string
	AmountPaid            float64
	CurrencyCode          string
	ServiceCode           string
}

var cellulantPaymentHandlers []func(CellulantPaymentEvent) error

func NewCellulantService(config CellulantConfig) *CellulantService {
	return &CellulantService{config: config}
}

func RegisterCellulantPaymentHandler(handler func(CellulantPaymentEvent) error) {
	cellulantPaymentHandlers = append(cellulantPaymentHandlers, handler)
}

// Encrypt reproduces the legacy PHP Tingg Express Checkout encryption:
// AES-256-CBC over JSON, Base64 from OpenSSL, then a second Base64 encoding.
func (s *CellulantService) Encrypt(payload []byte) (string, error) {
	secretHash := sha256.Sum256([]byte(s.config.SecretKey))
	secretHex := hex.EncodeToString(secretHash[:])
	key := []byte(secretHex[:32])

	ivHash := sha256.Sum256([]byte(s.config.IVKey))
	ivHex := hex.EncodeToString(ivHash[:])
	iv := []byte(ivHex[:aes.BlockSize])

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("creating Cellulant cipher: %w", err)
	}
	padded := pkcs7Pad(payload, block.BlockSize())
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(encrypted, padded)

	opensslBase64 := base64.StdEncoding.EncodeToString(encrypted)
	return base64.StdEncoding.EncodeToString([]byte(opensslBase64)), nil
}

func (s *CellulantService) CheckoutURL(request CellulantCheckoutRequest) (string, error) {
	if s.config.AccessKey == "" ||
		s.config.SecretKey == "" ||
		s.config.IVKey == "" ||
		s.config.CheckoutURL == "" {
		return "", fmt.Errorf("Cellulant checkout is not configured")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encoding Cellulant checkout payload: %w", err)
	}
	encrypted, err := s.Encrypt(payload)
	if err != nil {
		return "", err
	}
	checkoutURL, err := url.Parse(s.config.CheckoutURL)
	if err != nil {
		return "", fmt.Errorf("parsing Cellulant checkout URL: %w", err)
	}
	query := checkoutURL.Query()
	query.Set("encrypted_payload", encrypted)
	query.Set("access_key", s.config.AccessKey)
	checkoutURL.RawQuery = query.Encode()
	return checkoutURL.String(), nil
}

func pkcs7Pad(payload []byte, blockSize int) []byte {
	padding := blockSize - len(payload)%blockSize
	result := make([]byte, len(payload)+padding)
	copy(result, payload)
	for i := len(payload); i < len(result); i++ {
		result[i] = byte(padding)
	}
	return result
}
