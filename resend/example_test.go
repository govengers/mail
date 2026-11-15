package resend_test

import (
	"os"

	"github.com/govengers/mail"
	"github.com/govengers/mail/resend"
)

// The example only compiles: it needs an API key.
func ExampleNew() {
	transport, err := resend.New(&resend.Options{APIKey: os.Getenv("RESEND_API_KEY")})
	if err != nil {
		panic(err)
	}
	mailer, err := mail.New(&mail.Options{Transport: transport, From: "Tasks <noreply@example.com>"})
	if err != nil {
		panic(err)
	}
	_ = mailer
}
