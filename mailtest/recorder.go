// Package mailtest helps tests of code that sends mail: Recorder captures messages in memory,
// NewSMTPServer and NewResendServer are fakes that speak the protocols, for a test or an
// end-to-end suite that runs the real binary, and Links finds the links of a rendered mail.
package mailtest

import (
	"bytes"
	"context"
	"maps"
	"slices"
	"sync"

	"github.com/govengers/mail"
	"github.com/govengers/mail/internal/htmlscan"
)

// Recorder is a mail.Transport that keeps every message it accepts. The zero value is ready to
// use and safe for concurrent use.
type Recorder struct {
	mutex    sync.Mutex
	messages []*mail.Message
	err      error
}

// Send records a copy of the message, or returns the error FailWith set without recording.
func (r *Recorder) Send(_ context.Context, message *mail.Message) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if r.err != nil {
		return r.err
	}
	copied := *message
	copied.To = slices.Clone(message.To)
	copied.Headers = maps.Clone(message.Headers)
	copied.Attachments = slices.Clone(message.Attachments)
	for index := range copied.Attachments {
		copied.Attachments[index].Content = bytes.Clone(message.Attachments[index].Content)
	}
	r.messages = append(r.messages, &copied)
	return nil
}

// FailWith makes every following Send return err; nil accepts messages again.
func (r *Recorder) FailWith(err error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.err = err
}

// Messages returns the recorded messages in the order they were sent.
func (r *Recorder) Messages() []*mail.Message {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return slices.Clone(r.messages)
}

// Reset forgets the recorded messages.
func (r *Recorder) Reset() {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.messages = nil
}

// Links returns the link URLs (href, src, background, action, formaction, poster, srcset) of the
// message's HTML in order, to follow a confirmation link in a test.
func Links(message *mail.Message) []string {
	return htmlscan.Links(message.HTML)
}
