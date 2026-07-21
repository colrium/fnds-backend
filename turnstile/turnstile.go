package turnstile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"backend/configs"
)

// SiteVerifyURL is the Cloudflare Turnstile verification endpoint. Declared as a
// package var so tests can point it at a stub server.
var SiteVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

var (
	ErrInvalid     = errors.New("turnstile: invalid verification")
	ErrUnavailable = errors.New("turnstile: verification unavailable")
)

const maxTokenLength = 2048

var httpClient = &http.Client{Timeout: 10 * time.Second}

type siteVerifyResponse struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
	Hostname   string   `json:"hostname"`
	Action     string   `json:"action"`
}

// Verify validates a Turnstile token against Cloudflare's siteverify API.
// Unconfigured non-production environments skip verification for local
// development, while production fails closed.
func Verify(ctx context.Context, token, remoteIP, expectedAction string) error {
	secret := strings.TrimSpace(configs.TurnstileSecretKey())
	if secret == "" {
		if configs.EnvIsProd() {
			return fmt.Errorf("%w: secret key is not configured", ErrUnavailable)
		}
		return nil
	}

	token = strings.TrimSpace(token)
	if token == "" || len(token) > maxTokenLength {
		return ErrInvalid
	}

	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		SiteVerifyURL,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return fmt.Errorf("%w: create request: %v", ErrUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: siteverify request: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: siteverify status %d", ErrUnavailable, resp.StatusCode)
	}

	var result siteVerifyResponse
	limitedBody := io.LimitReader(resp.Body, 1<<16)
	if err := json.NewDecoder(limitedBody).Decode(&result); err != nil {
		return fmt.Errorf("%w: decode response: %v", ErrUnavailable, err)
	}
	if !result.Success {
		if hasConfigurationOrServiceError(result.ErrorCodes) {
			return fmt.Errorf("%w: error codes %v", ErrUnavailable, result.ErrorCodes)
		}
		return fmt.Errorf("%w: error codes %v", ErrInvalid, result.ErrorCodes)
	}
	if expectedAction != "" && result.Action != expectedAction {
		return fmt.Errorf("%w: action mismatch", ErrInvalid)
	}
	if expectedHostname := strings.TrimSpace(configs.TurnstileHostname()); expectedHostname != "" &&
		!strings.EqualFold(result.Hostname, expectedHostname) {
		return fmt.Errorf("%w: hostname mismatch", ErrInvalid)
	}
	return nil
}

func hasConfigurationOrServiceError(errorCodes []string) bool {
	for _, code := range errorCodes {
		switch code {
		case "internal-error", "missing-input-secret", "invalid-input-secret", "bad-request":
			return true
		}
	}
	return false
}
