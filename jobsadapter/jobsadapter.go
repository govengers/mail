// Package jobsadapter sends mail from a govengers/jobs job, so a mail is enqueued in the same
// transaction as the change that causes it and goes out after the commit, with retries. It is a
// module of its own, so the mail module does not depend on govengers/jobs.
//
// Declare the kind once, register the handler on the worker, and enqueue a rendered message in
// the caller's transaction:
//
//	var MailKind = jobs.NewKind[mail.Message]("mail.send", &jobs.KindOptions{Queue: "mail"})
//
//	err := jobsadapter.Handle(worker, MailKind, mailer)
//	_, err = store.Enqueue(ctx, transaction, MailKind.New(*message, nil))
//
// A job attempt sends with the job's ID as the message's IdempotencyKey unless the message has
// one, so the resend transport delivers once across retries (within Resend's 24 hours). The smtp
// transport has no such key: a retry after a lost reply delivers again, so SMTP is at least once.
// A failure that matches mail.ErrPermanent (an invalid message, a rejected recipient) ends the job
// as dead at once; every other failure retries with the kind's backoff.
//
// The job payload is the message as JSON, attachments included. govengers/jobs caps a payload at
// 256 KiB, so large attachments belong in blob storage, referenced from a payload of your own.
package jobsadapter

import (
	"context"
	"errors"

	"github.com/govengers/jobs"

	"github.com/govengers/mail"
)

// Handle registers the handler of kind on worker: it sends each job's message with transport,
// usually the service's *mail.Mailer.
func Handle(worker *jobs.Worker, kind *jobs.Kind[mail.Message], transport mail.Transport) error {
	if transport == nil {
		return errors.New("jobsadapter: Handle needs a transport")
	}
	return jobs.Handle(worker, kind, Handler(transport))
}

// Handler is the job handler Handle registers, for a worker that wraps it in its own handler.
func Handler(transport mail.Transport) func(ctx context.Context, job *jobs.Job[mail.Message]) error {
	return func(ctx context.Context, job *jobs.Job[mail.Message]) error {
		message := job.Payload
		if message.IdempotencyKey == "" {
			message.IdempotencyKey = job.ID.String()
		}
		err := transport.Send(ctx, &message)
		if errors.Is(err, mail.ErrPermanent) {
			return jobs.Permanent(err)
		}
		return err
	}
}
