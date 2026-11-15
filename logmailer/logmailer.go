// Package logmailer is the development transport: it writes every mail, headers, text part and
// HTML, to a writer instead of sending it, so a local run needs no mail server.
package logmailer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/govengers/mail"
)

// Options configures a Transport.
type Options struct {
	Writer io.Writer // required, e.g. os.Stderr
}

// Transport writes mail to a writer; it implements mail.Transport and is safe for concurrent use:
// one mail is one write.
type Transport struct {
	mutex  sync.Mutex
	writer io.Writer
}

// New returns a Transport.
func New(options *Options) (*Transport, error) {
	if options == nil || options.Writer == nil {
		return nil, errors.New("logmailer: Options.Writer is required")
	}
	return &Transport{writer: options.Writer}, nil
}

// Send writes the message and returns the writer's error.
func (t *Transport) Send(_ context.Context, message *mail.Message) error {
	var text strings.Builder
	text.WriteString("=== mail\n")
	fmt.Fprintf(&text, "From: %s\nTo: %s\n", message.From, strings.Join(message.To, ", "))
	if message.ReplyTo != "" {
		fmt.Fprintf(&text, "Reply-To: %s\n", message.ReplyTo)
	}
	fmt.Fprintf(&text, "Subject: %s\n", message.Subject)
	names := make([]string, 0, len(message.Headers))
	for name := range message.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&text, "%s: %s\n", name, message.Headers[name])
	}
	for _, attachment := range message.Attachments {
		fmt.Fprintf(&text, "Attachment: %s (%s, %d bytes)\n", attachment.Filename, attachment.ContentType, len(attachment.Content))
	}
	if message.Text != "" {
		fmt.Fprintf(&text, "--- text\n%s\n", strings.TrimRight(message.Text, "\n"))
	}
	if message.HTML != "" {
		fmt.Fprintf(&text, "--- html\n%s\n", strings.TrimRight(message.HTML, "\n"))
	}
	text.WriteString("=== end\n")
	t.mutex.Lock()
	defer t.mutex.Unlock()
	if _, err := io.WriteString(t.writer, text.String()); err != nil {
		return fmt.Errorf("logmailer: %w", err)
	}
	return nil
}
