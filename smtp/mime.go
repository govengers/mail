package smtp

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"sort"
	"strings"
	"time"

	netmail "net/mail"

	"github.com/govengers/mail"
)

// maximumLineLength is where a header line is folded (RFC 5322 recommends 78 characters).
const maximumLineLength = 78

// compose writes message as an RFC 5322 message with CRLF line endings: multipart/alternative
// for text and HTML, inside multipart/mixed with the attachments.
func compose(message *mail.Message, now time.Time) ([]byte, error) {
	from, err := netmail.ParseAddress(message.From)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	writeHeader(&buffer, "From", from.String())
	recipients, err := formatAddresses(message.To)
	if err != nil {
		return nil, err
	}
	writeHeader(&buffer, "To", recipients)
	if message.ReplyTo != "" {
		replyTo, err := formatAddresses([]string{message.ReplyTo})
		if err != nil {
			return nil, err
		}
		writeHeader(&buffer, "Reply-To", replyTo)
	}
	writeHeader(&buffer, "Subject", mime.QEncoding.Encode("utf-8", message.Subject))
	writeHeader(&buffer, "Date", now.Format(time.RFC1123Z))
	writeHeader(&buffer, "Message-ID", messageID(from.Address, message.IdempotencyKey))
	writeHeader(&buffer, "MIME-Version", "1.0")
	for _, name := range sortedNames(message.Headers) {
		writeHeader(&buffer, textproto.CanonicalMIMEHeaderKey(name), mime.QEncoding.Encode("utf-8", message.Headers[name]))
	}
	if err := writeBody(&buffer, message); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func formatAddresses(addresses []string) (string, error) {
	formatted := make([]string, 0, len(addresses))
	for _, address := range addresses {
		parsed, err := netmail.ParseAddress(address)
		if err != nil {
			return "", err
		}
		formatted = append(formatted, parsed.String())
	}
	return strings.Join(formatted, ", "), nil
}

// messageID returns a Message-ID in the sender's domain: derived from the idempotency key, so
// every retry of one job carries the same ID and receivers that deduplicate on it drop the second
// copy, or random without a key.
func messageID(from, idempotencyKey string) string {
	var local string
	if idempotencyKey != "" {
		sum := sha256.Sum256([]byte(idempotencyKey))
		local = hex.EncodeToString(sum[:])
	} else {
		random := make([]byte, 16)
		_, _ = rand.Read(random) // crypto/rand.Read never fails (Go 1.24)
		local = hex.EncodeToString(random)
	}
	return "<" + local + "@" + from[strings.LastIndexByte(from, '@')+1:] + ">"
}

// writeHeader writes one header field, folded at spaces so no line exceeds maximumLineLength
// where the value allows it. Values never hold line breaks (Message.Validate).
func writeHeader(buffer *bytes.Buffer, name, value string) {
	line := name + ":"
	for _, word := range strings.Split(value, " ") {
		if len(line)+1+len(word) > maximumLineLength && strings.TrimSpace(line) != name+":" {
			buffer.WriteString(line + "\r\n")
			line = ""
		}
		line += " " + word
	}
	buffer.WriteString(line + "\r\n")
}

// entity is a MIME entity: its header fields and its encoded body.
type entity struct {
	header textproto.MIMEHeader
	body   []byte
}

func writeBody(buffer *bytes.Buffer, message *mail.Message) error {
	alternatives, err := alternativesEntity(message)
	if err != nil {
		return err
	}
	if len(message.Attachments) == 0 {
		writeEntity(buffer, alternatives)
		return nil
	}
	var body bytes.Buffer
	mixed := multipart.NewWriter(&body)
	part, err := mixed.CreatePart(alternatives.header)
	if err != nil {
		return err
	}
	if _, err := part.Write(alternatives.body); err != nil {
		return err
	}
	for index := range message.Attachments {
		if err := writeAttachment(mixed, &message.Attachments[index]); err != nil {
			return err
		}
	}
	if err := mixed.Close(); err != nil {
		return err
	}
	writeEntity(buffer, &entity{header: multipartHeader("multipart/mixed", mixed.Boundary()), body: body.Bytes()})
	return nil
}

// writeEntity writes the entity as the rest of the message: its header fields, a blank line, its body.
func writeEntity(buffer *bytes.Buffer, part *entity) {
	for _, name := range []string{"Content-Type", "Content-Transfer-Encoding"} {
		if value := part.header.Get(name); value != "" {
			writeHeader(buffer, name, value)
		}
	}
	buffer.WriteString("\r\n")
	buffer.Write(part.body)
}

// alternativesEntity is the text part, the HTML part, or both as multipart/alternative.
func alternativesEntity(message *mail.Message) (*entity, error) {
	switch {
	case message.HTML == "":
		return textEntity("text/plain", message.Text)
	case message.Text == "":
		return textEntity("text/html", message.HTML)
	}
	var body bytes.Buffer
	alternative := multipart.NewWriter(&body)
	for _, content := range []struct{ mediaType, text string }{{"text/plain", message.Text}, {"text/html", message.HTML}} {
		part, err := textEntity(content.mediaType, content.text)
		if err != nil {
			return nil, err
		}
		writer, err := alternative.CreatePart(part.header)
		if err != nil {
			return nil, err
		}
		if _, err := writer.Write(part.body); err != nil {
			return nil, err
		}
	}
	if err := alternative.Close(); err != nil {
		return nil, err
	}
	return &entity{header: multipartHeader("multipart/alternative", alternative.Boundary()), body: body.Bytes()}, nil
}

func multipartHeader(mediaType, boundary string) textproto.MIMEHeader {
	return textproto.MIMEHeader{"Content-Type": {mime.FormatMediaType(mediaType, map[string]string{"boundary": boundary})}}
}

func textEntity(mediaType, text string) (*entity, error) {
	var body bytes.Buffer
	if err := writeQuotedPrintable(&body, text); err != nil {
		return nil, err
	}
	return &entity{
		header: textproto.MIMEHeader{
			"Content-Type":              {mediaType + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		},
		body: body.Bytes(),
	}, nil
}

func writeQuotedPrintable(writer io.Writer, content string) error {
	encoder := quotedprintable.NewWriter(writer)
	if _, err := encoder.Write([]byte(content)); err != nil {
		return err
	}
	return encoder.Close()
}

func writeAttachment(mixed *multipart.Writer, attachment *mail.Attachment) error {
	contentType := attachment.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	part, err := mixed.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {contentType},
		"Content-Transfer-Encoding": {"base64"},
		"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Filename})},
	})
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(attachment.Content)
	for len(encoded) > 76 {
		if _, err := io.WriteString(part, encoded[:76]+"\r\n"); err != nil {
			return err
		}
		encoded = encoded[76:]
	}
	_, err = io.WriteString(part, encoded+"\r\n")
	return err
}

func sortedNames(headers map[string]string) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
