package mailtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/govengers/mail"
	"github.com/govengers/mail/internal/resendapi"
)

// ResendServerOptions configures a fake Resend API.
type ResendServerOptions struct {
	APIKey string // the expected bearer token; empty accepts any
	// Reply, when set, sees every well-formed request and returns a status and an error name to
	// answer with instead of accepting it (429, "rate_limit_exceeded"); status 0 accepts.
	Reply func(request *ResendRequest) (status int, errorName string)
}

// ResendRequest is one POST /emails the fake Resend API received.
type ResendRequest struct {
	IdempotencyKey string
	Message        *mail.Message // From, To, ReplyTo, Subject, HTML, Text, Headers, Attachments
}

// ResendServer is a fake of Resend's POST /emails on an httptest server. Like Resend, it delivers a
// request whose Idempotency-Key it has seen once: it answers with the first delivery's ID, and
// with 409 invalid_idempotent_request when the payload differs.
type ResendServer struct {
	server    *httptest.Server
	options   ResendServerOptions
	mutex     sync.Mutex
	requests  []*ResendRequest
	delivered []*mail.Message
	keys      map[string]idempotentDelivery
}

type idempotentDelivery struct {
	id   string
	body []byte
}

// NewResendServer starts a fake Resend API; tb's cleanup stops it. Point the resend transport's
// Options.BaseURL at URL. A nil options value accepts every request.
func NewResendServer(tb testing.TB, options *ResendServerOptions) *ResendServer {
	tb.Helper()
	fake := &ResendServer{keys: map[string]idempotentDelivery{}}
	if options != nil {
		fake.options = *options
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	tb.Cleanup(fake.server.Close)
	return fake
}

// URL is the base URL of the fake API.
func (s *ResendServer) URL() string {
	return s.server.URL
}

// Requests returns every well-formed request in order, retries included.
func (s *ResendServer) Requests() []*ResendRequest {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return append([]*ResendRequest(nil), s.requests...)
}

// Messages returns the delivered messages in order: one per idempotency key.
func (s *ResendServer) Messages() []*mail.Message {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return append([]*mail.Message(nil), s.delivered...)
}

func (s *ResendServer) serve(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.URL.Path != "/emails" {
		writeResendError(writer, http.StatusNotFound, "not_found", "only POST /emails")
		return
	}
	if s.options.APIKey != "" && request.Header.Get("Authorization") != "Bearer "+s.options.APIKey {
		writeResendError(writer, http.StatusUnauthorized, "missing_api_key", "wrong API key")
		return
	}
	body, err := io.ReadAll(request.Body)
	var email resendapi.Email
	if err != nil || json.Unmarshal(body, &email) != nil {
		writeResendError(writer, http.StatusUnprocessableEntity, "validation_error", "the body is not an email")
		return
	}
	received := &ResendRequest{IdempotencyKey: request.Header.Get("Idempotency-Key"), Message: messageOf(&email)}
	if s.options.Reply != nil {
		if status, name := s.options.Reply(received); status != 0 {
			s.record(received)
			writeResendError(writer, status, name, "rejected by the test")
			return
		}
	}
	id, conflict := s.deliver(received, body)
	if conflict {
		writeResendError(writer, http.StatusConflict, resendapi.ErrorInvalidIdempotentRequest, "the key was used with another payload")
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(resendapi.Sent{ID: id})
}

func (s *ResendServer) record(received *ResendRequest) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.requests = append(s.requests, received)
}

// deliver records the request and delivers it unless its key was seen; it reports the
// delivery's ID and whether the key was seen with another body.
func (s *ResendServer) deliver(received *ResendRequest, body []byte) (string, bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.requests = append(s.requests, received)
	if earlier, seen := s.keys[received.IdempotencyKey]; seen && received.IdempotencyKey != "" {
		return earlier.id, !bytes.Equal(earlier.body, body)
	}
	s.delivered = append(s.delivered, received.Message)
	id := fmt.Sprintf("mailtest-%d", len(s.delivered))
	if received.IdempotencyKey != "" {
		s.keys[received.IdempotencyKey] = idempotentDelivery{id: id, body: body}
	}
	return id, false
}

func messageOf(email *resendapi.Email) *mail.Message {
	message := &mail.Message{
		From: email.From, To: email.To, ReplyTo: strings.Join(email.ReplyTo, ", "), Subject: email.Subject,
		HTML: email.HTML, Text: email.Text, Headers: email.Headers,
	}
	for _, attachment := range email.Attachments {
		message.Attachments = append(message.Attachments, mail.Attachment{
			Filename: attachment.Filename, ContentType: attachment.ContentType, Content: attachment.Content,
		})
	}
	return message
}

func writeResendError(writer http.ResponseWriter, status int, name, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(resendapi.Error{StatusCode: status, Name: name, Message: message})
}
