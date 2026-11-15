package logmailer_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/govengers/mail"
	"github.com/govengers/mail/logmailer"
)

func TestSendWritesTheWholeMail(t *testing.T) {
	var output strings.Builder
	transport, err := logmailer.New(&logmailer.Options{Writer: &output})
	require.NoError(t, err)

	require.NoError(t, transport.Send(t.Context(), &mail.Message{
		From: "a@example.com", To: []string{"b@example.com", "c@example.com"}, ReplyTo: "r@example.com", Subject: "Hi",
		Text: "text part\n", HTML: "<p>html part</p>", Headers: map[string]string{"X-B": "2", "X-A": "1"},
		Attachments: []mail.Attachment{{Filename: "a.pdf", ContentType: "application/pdf", Content: []byte("1234")}},
	}))

	require.Equal(t, `=== mail
From: a@example.com
To: b@example.com, c@example.com
Reply-To: r@example.com
Subject: Hi
X-A: 1
X-B: 2
Attachment: a.pdf (application/pdf, 4 bytes)
--- text
text part
--- html
<p>html part</p>
=== end
`, output.String())
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestSendReturnsTheWritersError(t *testing.T) {
	transport, err := logmailer.New(&logmailer.Options{Writer: failingWriter{}})
	require.NoError(t, err)

	require.ErrorContains(t, transport.Send(t.Context(), &mail.Message{Subject: "x"}), "disk full")
}

func TestNewNeedsAWriter(t *testing.T) {
	_, err := logmailer.New(nil)
	require.Error(t, err)
	_, err = logmailer.New(&logmailer.Options{})
	require.Error(t, err)
}
