package mailtest

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	netmail "net/mail"
	"strings"

	"github.com/govengers/mail"
)

// structuralHeaders are the fields Parse turns into Message fields or drops instead of copying
// them into Message.Headers.
var structuralHeaders = map[string]bool{
	"Content-Transfer-Encoding": true, "Content-Type": true, "Date": true, "From": true, "Message-Id": true,
	"Mime-Version": true, "Reply-To": true, "Subject": true, "To": true,
}

// Parse reads an RFC 5322 message, as the smtp transport writes it, back into a Message: decoded
// subject and display names ("Ada <ada@example.com>"), the text and HTML parts with LF line
// endings, attachments, and the remaining header fields. A message of a single part ends with the
// line break SMTP's DATA terminator needs, which the part may not have had.
func Parse(data []byte) (*mail.Message, error) {
	raw, err := netmail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("mailtest: parse: %w", err)
	}
	decoder := &mime.WordDecoder{}
	subject, err := decoder.DecodeHeader(raw.Header.Get("Subject"))
	if err != nil {
		return nil, fmt.Errorf("mailtest: parse the subject: %w", err)
	}
	message := &mail.Message{Subject: subject}
	if message.From, err = addresses(raw.Header, "From", false); err != nil {
		return nil, err
	}
	if message.ReplyTo, err = addresses(raw.Header, "Reply-To", true); err != nil {
		return nil, err
	}
	recipients, err := raw.Header.AddressList("To")
	if err != nil {
		return nil, fmt.Errorf("mailtest: parse To: %w", err)
	}
	for _, recipient := range recipients {
		message.To = append(message.To, display(recipient))
	}
	for name, values := range raw.Header {
		if !structuralHeaders[name] {
			if message.Headers == nil {
				message.Headers = map[string]string{}
			}
			message.Headers[name], _ = decoder.DecodeHeader(values[0])
		}
	}
	if err := readEntity(message, raw.Header.Get("Content-Type"), raw.Header.Get("Content-Transfer-Encoding"), "", raw.Body); err != nil {
		return nil, err
	}
	return message, nil
}

func addresses(header netmail.Header, name string, optional bool) (string, error) {
	if optional && header.Get(name) == "" {
		return "", nil
	}
	address, err := netmail.ParseAddress(header.Get(name))
	if err != nil {
		return "", fmt.Errorf("mailtest: parse %s: %w", name, err)
	}
	return display(address), nil
}

func display(address *netmail.Address) string {
	if address.Name == "" {
		return address.Address
	}
	return address.Name + " <" + address.Address + ">"
}

// readEntity reads one MIME entity into message: a multipart one part by part, a leaf as the
// text part, the HTML part or an attachment.
func readEntity(message *mail.Message, contentType, encoding, disposition string, body io.Reader) error {
	mediaType, parameters, err := mime.ParseMediaType(contentType)
	if err != nil {
		return fmt.Errorf("mailtest: parse the content type %q: %w", contentType, err)
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		return readMultipart(message, multipart.NewReader(body, parameters["boundary"]))
	}
	content, err := io.ReadAll(decoded(body, encoding))
	if err != nil {
		return fmt.Errorf("mailtest: read a %s part: %w", mediaType, err)
	}
	if kind, dispositionParameters, err := mime.ParseMediaType(disposition); err == nil && kind == "attachment" {
		message.Attachments = append(message.Attachments, mail.Attachment{
			Filename: dispositionParameters["filename"], ContentType: contentType, Content: content,
		})
		return nil
	}
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	switch mediaType {
	case "text/plain":
		message.Text = text
	case "text/html":
		message.HTML = text
	default:
		return fmt.Errorf("mailtest: a %s part that is not an attachment", mediaType)
	}
	return nil
}

func readMultipart(message *mail.Message, reader *multipart.Reader) error {
	for {
		part, err := reader.NextPart() // decodes quoted-printable itself
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("mailtest: read a part: %w", err)
		}
		header := part.Header
		if err := readEntity(message, header.Get("Content-Type"), header.Get("Content-Transfer-Encoding"), header.Get("Content-Disposition"), part); err != nil {
			return err
		}
	}
}

func decoded(body io.Reader, encoding string) io.Reader {
	switch strings.ToLower(encoding) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, body) // the decoder skips line breaks
	case "quoted-printable":
		return quotedprintable.NewReader(body)
	default:
		return body
	}
}
