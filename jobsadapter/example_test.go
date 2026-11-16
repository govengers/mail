package jobsadapter_test

import (
	"context"

	"github.com/govengers/jobs"
	"github.com/govengers/jobs/postgresadapter"
	"github.com/jackc/pgx/v5"

	"github.com/govengers/mail"
	"github.com/govengers/mail/jobsadapter"
)

// main builds the Sender and registers its handler; a service method enqueues the mail in the
// transaction of the change that causes it. The job renders it. The example only compiles: it
// needs a database.
func ExampleSender_Template() {
	var worker *jobs.Worker          // jobs.NewWorker on the service's postgresadapter.Store
	var store *postgresadapter.Store // postgresadapter.New on the service's pool
	var mailer *mail.Mailer          // mail.New with the smtp or resend transport
	var templates *mail.Templates    // mail.NewTemplates on the embedded, compiled templates
	var sealer jobsadapter.Sealer    // the service's key ring, so the reset link is sealed in the payload
	var transaction pgx.Tx           // the transaction that creates the reset request
	ctx := context.Background()

	sender, err := jobsadapter.New(&jobsadapter.Options{Transport: mailer, Templates: templates, Sealer: sealer})
	if err != nil {
		panic(err)
	}
	if err := sender.Handle(worker, jobsadapter.Kind); err != nil {
		panic(err)
	}

	data := map[string]string{"Name": "Ada", "ResetURL": "https://tasks.example.com/reset#token=..."}
	payload, err := sender.Template("reset", "en", data, &mail.Message{To: []string{"ada@example.com"}})
	if err != nil {
		return
	}
	if _, err := store.Enqueue(ctx, transaction, jobsadapter.Kind.New(payload, nil)); err != nil {
		return
	}
}
