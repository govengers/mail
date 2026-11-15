package mail_test

import (
	"context"
	"fmt"
	"os"

	"github.com/govengers/mail"
	"github.com/govengers/mail/mailtest"
)

// A service builds one Mailer in main and hands it to the code that sends; tests swap the
// transport for a mailtest.Recorder.
func ExampleNew() {
	recorder := &mailtest.Recorder{} // smtp.New or resend.New in production, logmailer.New in development
	mailer, err := mail.New(&mail.Options{Transport: recorder, From: "Tasks <noreply@example.com>"})
	if err != nil {
		panic(err)
	}

	err = mailer.Send(context.Background(), &mail.Message{To: []string{"ada@example.com"}, Subject: "Hello", Text: "Hi Ada\n"})

	fmt.Println(err, recorder.Messages()[0].From)
	// Output: <nil> Tasks <noreply@example.com>
}

// Templates are compiled by cmd/mailgen and usually embedded; Render picks the locale, the caller
// adds the recipients.
func ExampleTemplates_Render() {
	templates, err := mail.NewTemplates(&mail.TemplatesOptions{FS: os.DirFS("testdata/templates"), DefaultLocale: "en"})
	if err != nil {
		panic(err)
	}

	message, err := templates.Render("welcome", "de-AT", map[string]any{
		"Product": "Tasks", "Name": "Ada", "ConfirmURL": "https://tasks.example.com/confirm?token=abc", "Tasks": []string{},
	})
	if err != nil {
		panic(err)
	}
	message.To = []string{"ada@example.com"}

	fmt.Println(message.Subject)
	for _, link := range mailtest.Links(message) {
		fmt.Println(link)
	}
	// Output:
	// Willkommen bei Tasks, Ada
	// https://tasks.example.com/confirm?token=abc
	// mailto:support@example.com
}
