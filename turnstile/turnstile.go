package turnstile

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"backend/configs"
)

// SiteVerifyURL is the Cloudflare Turnstile verification endpoint. Declared as a
// package var so tests can point it at a stub server.
var SiteVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

type siteVerifyResponse struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
}

// Verify validates a Turnstile token against Cloudflare's siteverify API. When
// TURNSTILE_SECRET_KEY is empty, verification is skipped and nil is returned so
// local dev and tests run without keys.
func Verify(token, remoteIP string) error {
	secret := configs.TurnstileSecretKey()
	if secret == "" {
		return nil
	}
	if token == "" {
		return errors.New("turnstile: missing token")
	}

	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(SiteVerifyURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("turnstile: siteverify request failed: %w", err)
	}
	defer resp.Body.Close()

	var result siteVerifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("turnstile: decode response failed: %w", err)
	}
	if !result.Success {
		return fmt.Errorf("turnstile: verification failed: %v", result.ErrorCodes)
	}
	return nil
}
