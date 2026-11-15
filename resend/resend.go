// Package resend sends mail through the Resend HTTP API (POST /emails) with net/http. A message's
// IdempotencyKey goes into the Idempotency-Key header, so a retry within 24 hours delivers once.
package resend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/govengers/mail"
	"github.com/govengers/mail/internal/resendapi"
)

// DefaultBaseURL is Resend's API.
const DefaultBaseURL = "https://api.resend.com"

// DefaultTimeout is the timeout of the HTTP client New builds when Options.HTTPClient is nil.
const DefaultTimeout = 30 * time.Second

// maximumErrorBody bounds how much of an error response Send reads.
const maximumErrorBody = 64 << 10

// Options configures a Transport.
type Options struct {
	APIKey  string // required
	BaseURL string // empty means DefaultBaseURL; a test points it at mailtest.NewResendServer
	// HTTPClient sends the requests; nil means a client with DefaultTimeout.
	HTTPClient *http.Client
}

// Transport sends mail through Resend; it implements mail.Transport and is safe for concurrent use.
type Transport struct {
	apiKey   string
	endpoint string
	client   *http.Client
}

// New returns a Transport. It does not call the API.
func New(options *Options) (*Transport, error) {
	if options == nil || options.APIKey == "" {
		return nil, errors.New("resend: Options.APIKey is required")
	}
	baseURL := options.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if !strings.HasPrefix(baseURL, "https://") && !strings.HasPrefix(baseURL, "http://") {
		return nil, fmt.Errorf("resend: Options.BaseURL %q is not an http or https URL", baseURL)
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	return &Transport{apiKey: options.APIKey, endpoint: strings.TrimSuffix(baseURL, "/") + "/emails", client: client}, nil
}

// Send posts the message. A 4xx response matches mail.ErrPermanent, except 401, 403 and 404 (the
// transport's configuration), 408, 429 and a 409 for a request with the same key still in flight,
// which may succeed later like a 5xx or a network error.
func (t *Transport) Send(ctx context.Context, message *mail.Message) error {
	body, err := json.Marshal(email(message))
	if err != nil {
		return fmt.Errorf("resend: encode the message: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("resend: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+t.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "govengers-mail")
	if message.IdempotencyKey != "" {
		request.Header.Set("Idempotency-Key", message.IdempotencyKey)
	}
	response, err := t.client.Do(request)
	if err != nil {
		return fmt.Errorf("resend: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumErrorBody)) // lets the client reuse the connection
		return nil
	}
	return failure(response)
}

func email(message *mail.Message) *resendapi.Email {
	payload := &resendapi.Email{
		From:    message.From,
		To:      message.To,
		Subject: message.Subject,
		HTML:    message.HTML,
		Text:    message.Text,
		Headers: message.Headers,
	}
	if message.ReplyTo != "" {
		payload.ReplyTo = []string{message.ReplyTo}
	}
	for _, attachment := range message.Attachments {
		payload.Attachments = append(payload.Attachments, resendapi.Attachment{
			Filename: attachment.Filename, Content: attachment.Content, ContentType: attachment.ContentType,
		})
	}
	return payload
}

func failure(response *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(response.Body, maximumErrorBody))
	var body resendapi.Error
	if json.Unmarshal(raw, &body) != nil || body.Name == "" {
		body.Message = strings.TrimSpace(string(raw))
	}
	err := fmt.Errorf("resend: %d %s: %s", response.StatusCode, body.Name, body.Message)
	if permanent(response.StatusCode, body.Name) {
		return fmt.Errorf("%w: %w", mail.ErrPermanent, err)
	}
	return err
}

// permanent reports whether the response rejects the message itself. 401, 403 and 404 say the
// transport is misconfigured (API key, unverified domain, base URL); the message goes out once
// that is fixed, so they are temporary like 408 and 429.
func permanent(status int, name string) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	case http.StatusConflict:
		return name != resendapi.ErrorConcurrentIdempotentRequests
	default:
		return status >= 400 && status < 500
	}
}
