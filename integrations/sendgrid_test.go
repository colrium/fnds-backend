package integrations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestSendGridClientSend(t *testing.T) {
	var gotAuthorization string
	var gotPayload sendGridRequest

	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotAuthorization = request.Header.Get("Authorization")
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		if err := json.Unmarshal(body, &gotPayload); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	})}

	client := NewSendGridClient(
		"sendgrid-key",
		"notifications@fundis.co.ke",
		"Fundis",
		WithSendGridHTTPClient(httpClient),
	)
	err := client.Send(
		context.Background(),
		"employer@example.com",
		"New application",
		"<p>A worker applied.</p>",
	)
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	if gotAuthorization != "Bearer sendgrid-key" {
		t.Fatalf("Authorization = %q, want bearer token", gotAuthorization)
	}
	if len(gotPayload.Personalizations) != 1 ||
		len(gotPayload.Personalizations[0].To) != 1 ||
		gotPayload.Personalizations[0].To[0].Email != "employer@example.com" {
		t.Fatalf("unexpected personalizations: %#v", gotPayload.Personalizations)
	}
	if gotPayload.From.Email != "notifications@fundis.co.ke" {
		t.Fatalf("from email = %q", gotPayload.From.Email)
	}
	if gotPayload.Subject != "New application" {
		t.Fatalf("subject = %q", gotPayload.Subject)
	}
}

func TestSendGridClientSendRejectsMissingConfiguration(t *testing.T) {
	client := NewSendGridClient("", "", "")
	if err := client.Send(context.Background(), "employer@example.com", "Subject", "Body"); err == nil {
		t.Fatal("expected missing configuration error")
	}
}

func TestSendGridClientSendReturnsProviderError(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body: io.NopCloser(strings.NewReader(
				`{"errors":[{"message":"invalid sender"}]}`,
			)),
			Header: make(http.Header),
		}, nil
	})}

	client := NewSendGridClient(
		"sendgrid-key",
		"notifications@fundis.co.ke",
		"Fundis",
		WithSendGridHTTPClient(httpClient),
	)
	if err := client.Send(context.Background(), "employer@example.com", "Subject", "Body"); err == nil {
		t.Fatal("expected provider error")
	}
}
