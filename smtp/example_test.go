package smtp_test

import (
	"os"

	"github.com/govengers/mail"
	"github.com/govengers/mail/smtp"
)

// main builds the transport from configuration and gives it to the Mailer. The example only
// compiles: it needs a mail server.
func ExampleNew() {
	transport, err := smtp.New(&smtp.Options{
		Host:     "smtp.example.com",
		Username: "tasks",
		Password: os.Getenv("SMTP_PASSWORD"), // main reads configuration, never the library
	})
	if err != nil {
		panic(err)
	}
	mailer, err := mail.New(&mail.Options{Transport: transport, From: "Tasks <noreply@example.com>"})
	if err != nil {
		panic(err)
	}
	_ = mailer
}
