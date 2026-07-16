package turnstile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
		log.Printf("turnstile: siteverify request failed: %v", err)
		return fmt.Errorf("turnstile: siteverify request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("turnstile: siteverify returned status %d", resp.StatusCode)
		return fmt.Errorf("turnstile: siteverify returned status %d", resp.StatusCode)
	}

	var result siteVerifyResponse
	limitedBody := io.LimitReader(resp.Body, 1<<16)
	if err := json.NewDecoder(limitedBody).Decode(&result); err != nil {
		log.Printf("turnstile: decode response failed: %v", err)
		return fmt.Errorf("turnstile: decode response failed: %w", err)
	}
	if !result.Success {
		return fmt.Errorf("turnstile: verification failed: %v", result.ErrorCodes)
	}
	return nil
}
