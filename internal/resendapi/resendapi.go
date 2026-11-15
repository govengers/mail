// Package resendapi holds the request and error bodies of Resend's POST /emails, shared by the
// resend transport and the fake server in mailtest.
package resendapi

// Email is the request body of POST /emails.
type Email struct {
	From        string            `json:"from"`
	To          []string          `json:"to"`
	Subject     string            `json:"subject"`
	HTML        string            `json:"html,omitempty"`
	Text        string            `json:"text,omitempty"`
	ReplyTo     []string          `json:"reply_to,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Attachments []Attachment      `json:"attachments,omitempty"`
}

// Attachment is one attachment of an Email; Content is base64 (encoding/json's []byte form).
type Attachment struct {
	Filename    string `json:"filename"`
	Content     []byte `json:"content"`
	ContentType string `json:"content_type,omitempty"`
}

// Sent is the response body of a successful POST /emails.
type Sent struct {
	ID string `json:"id"`
}

// Error is the response body of a failed request.
type Error struct {
	StatusCode int    `json:"statusCode"`
	Name       string `json:"name"`
	Message    string `json:"message"`
}

// The error names whose status alone does not tell whether a retry can succeed.
const (
	ErrorConcurrentIdempotentRequests = "concurrent_idempotent_requests" // 409: the same key is in flight; retry
	ErrorInvalidIdempotentRequest     = "invalid_idempotent_request"     // 409: the key was used with another payload
)
