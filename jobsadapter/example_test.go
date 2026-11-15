package jobsadapter_test

import (
	"context"

	"github.com/govengers/jobs"
	"github.com/govengers/jobs/postgresadapter"
	"github.com/jackc/pgx/v5"

	"github.com/govengers/mail"
	"github.com/govengers/mail/jobsadapter"
)

// MailKind is the service's one kind for outgoing mail.
var MailKind = jobs.NewKind[mail.Message]("mail.send", &jobs.KindOptions{Queue: "mail"})

// main registers the handler; a service method renders the mail and enqueues it in the
// transaction of the change that causes it. The example only compiles: it needs a database.
func ExampleHandle() {
	var worker *jobs.Worker          // jobs.NewWorker on the service's postgresadapter.Store
	var store *postgresadapter.Store // postgresadapter.New on the service's pool
	var mailer *mail.Mailer          // mail.New with the smtp or resend transport
	var templates *mail.Templates    // mail.NewTemplates on the embedded, compiled templates
	var transaction pgx.Tx           // the transaction that creates the user
	ctx := context.Background()

	if err := jobsadapter.Handle(worker, MailKind, mailer); err != nil {
		panic(err)
	}

	message, err := templates.Render("welcome", "en", map[string]string{"Name": "Ada"})
	if err != nil {
		return
	}
	message.To = []string{"ada@example.com"}
	if _, err := store.Enqueue(ctx, transaction, MailKind.New(*message, nil)); err != nil {
		return
	}
}
