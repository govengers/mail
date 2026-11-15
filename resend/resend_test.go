package resend_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/govengers/mail"
	"github.com/govengers/mail/mailtest"
	"github.com/govengers/mail/resend"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func newTransport(t *testing.T, baseURL string) *resend.Transport {
	t.Helper()
	client := &http.Client{}
	t.Cleanup(client.CloseIdleConnections)
	transport, err := resend.New(&resend.Options{APIKey: "re_test", BaseURL: baseURL, HTTPClient: client})
	require.NoError(t, err)
	return transport
}

func TestSendPostsEveryField(t *testing.T) {
	server := mailtest.NewResendServer(t, &mailtest.ResendServerOptions{APIKey: "re_test"})
	transport := newTransport(t, server.URL()+"/")
	message := &mail.Message{
		From: "Tasks <noreply@example.com>", To: []string{"ada@example.com"}, ReplyTo: "help@example.com",
		Subject: "Hi", HTML: "<p>Hi</p>", Text: "Hi\n", Headers: map[string]string{"X-Entity-Ref-ID": "42"},
		Attachments:    []mail.Attachment{{Filename: "a.txt", ContentType: "text/plain", Content: []byte("hello")}},
		IdempotencyKey: "job-1",
	}

	require.NoError(t, transport.Send(t.Context(), message))

	requests := server.Requests()
	require.Len(t, requests, 1)
	require.Equal(t, "job-1", requests[0].IdempotencyKey)
	delivered := requests[0].Message
	delivered.IdempotencyKey = message.IdempotencyKey
	require.Equal(t, message, delivered)
}

func TestARetryWithTheSameKeyDeliversOnce(t *testing.T) {
	server := mailtest.NewResendServer(t, nil)
	transport := newTransport(t, server.URL())
	message := &mail.Message{From: "a@example.com", To: []string{"b@example.com"}, Subject: "Hi", Text: "x", IdempotencyKey: "job-1"}

	require.NoError(t, transport.Send(t.Context(), message))
	require.NoError(t, transport.Send(t.Context(), message))

	require.Len(t, server.Requests(), 2)
	require.Len(t, server.Messages(), 1)

	message.Subject = "Changed"
	err := transport.Send(t.Context(), message)
	require.ErrorIs(t, err, mail.ErrPermanent, "the key was used with another payload")
}

func TestSendClassifiesResponses(t *testing.T) {
	for name, testCase := range map[string]struct {
		status    int
		errorName string
		permanent bool
	}{
		"validation":        {status: http.StatusUnprocessableEntity, errorName: "validation_error", permanent: true},
		"forbidden":         {status: http.StatusForbidden, errorName: "invalid_api_key"},
		"missing key":       {status: http.StatusUnauthorized, errorName: "missing_api_key"},
		"wrong base URL":    {status: http.StatusNotFound, errorName: "not_found"},
		"rate limit":        {status: http.StatusTooManyRequests, errorName: "rate_limit_exceeded"},
		"timeout":           {status: http.StatusRequestTimeout, errorName: "timeout"},
		"concurrent key":    {status: http.StatusConflict, errorName: "concurrent_idempotent_requests"},
		"key reused":        {status: http.StatusConflict, errorName: "invalid_idempotent_request", permanent: true},
		"server error":      {status: http.StatusInternalServerError, errorName: "internal_server_error"},
		"unavailable":       {status: http.StatusServiceUnavailable},
		"without a payload": {status: http.StatusBadRequest, permanent: true},
	} {
		server := mailtest.NewResendServer(t, &mailtest.ResendServerOptions{Reply: func(*mailtest.ResendRequest) (int, string) {
			return testCase.status, testCase.errorName
		}})
		transport := newTransport(t, server.URL())

		err := transport.Send(t.Context(), &mail.Message{From: "a@example.com", To: []string{"b@example.com"}, Subject: "Hi", Text: "x"})

		require.Error(t, err, name)
		require.Equal(t, testCase.permanent, errors.Is(err, mail.ErrPermanent), name)
		require.Len(t, server.Requests(), 1, name)
		require.Empty(t, server.Messages(), name)
	}
}

func TestSendReportsAnErrorBodyThatIsNotJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "bad gateway from the proxy", http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)
	transport := newTransport(t, server.URL)

	err := transport.Send(t.Context(), &mail.Message{From: "a@example.com", To: []string{"b@example.com"}, Subject: "Hi", Text: "x"})

	require.ErrorContains(t, err, "502")
	require.ErrorContains(t, err, "bad gateway from the proxy")
	require.NotErrorIs(t, err, mail.ErrPermanent)
}

func TestSendRejectsAWrongAPIKey(t *testing.T) {
	server := mailtest.NewResendServer(t, &mailtest.ResendServerOptions{APIKey: "re_other"})
	transport := newTransport(t, server.URL())

	err := transport.Send(t.Context(), &mail.Message{From: "a@example.com", To: []string{"b@example.com"}, Subject: "Hi", Text: "x"})

	require.NotErrorIs(t, err, mail.ErrPermanent, "a wrong key must not kill the queued mails")
	require.Empty(t, server.Requests())
}

func TestSendReturnsTheContextsError(t *testing.T) {
	server := mailtest.NewResendServer(t, nil)
	transport := newTransport(t, server.URL())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := transport.Send(ctx, &mail.Message{From: "a@example.com", To: []string{"b@example.com"}, Subject: "Hi", Text: "x"})

	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, mail.ErrPermanent)
}

func TestNewValidatesItsOptions(t *testing.T) {
	_, err := resend.New(nil)
	require.Error(t, err)
	_, err = resend.New(&resend.Options{})
	require.Error(t, err)
	_, err = resend.New(&resend.Options{APIKey: "k", BaseURL: "api.resend.com"})
	require.Error(t, err)
	transport, err := resend.New(&resend.Options{APIKey: "k"})
	require.NoError(t, err)
	require.NotNil(t, transport)
}
