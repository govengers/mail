package jobsadapter_test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/govengers/jobs"
	"github.com/govengers/jobs/jobstest"
	"github.com/stretchr/testify/require"

	"github.com/govengers/mail"
	"github.com/govengers/mail/jobsadapter"
	"github.com/govengers/mail/mailtest"
)

// store is an in-memory jobs.Store holding the claims of one queue; it records how each ended.
// The real Store and the enqueue in the caller's transaction are govengers/jobs' own tested
// contract; this fake keeps the adapter's tests free of a database.
type store struct {
	mutex    sync.Mutex
	pending  []*jobs.Claim
	finished map[uuid.UUID]*jobs.Finish
	payloads map[uuid.UUID][]byte
}

// enqueue stores the payload as JSON, as the Postgres store does, and returns the job's ID.
func (s *store) enqueue(t *testing.T, payload jobsadapter.Payload) uuid.UUID {
	t.Helper()
	encoded, err := jobsadapter.Kind.New(payload, nil).EncodePayload()
	require.NoError(t, err)
	id := uuid.New()
	s.payloads[id] = encoded
	s.pending = append(s.pending, &jobs.Claim{
		Lease: jobs.Lease{JobID: id, Token: uuid.New()}, Queue: jobsadapter.Kind.Queue(), Kind: jobsadapter.Kind.Name(),
		Payload: encoded, Attempt: 1, MaximumAttempts: jobs.DefaultMaximumAttempts,
	})
	return id
}

func (s *store) Claim(_ context.Context, request *jobs.ClaimRequest) ([]*jobs.Claim, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	count := min(request.Limit, len(s.pending))
	claims := s.pending[:count]
	s.pending = s.pending[count:]
	return claims, nil
}

func (s *store) Finish(_ context.Context, finish *jobs.Finish) (jobs.Status, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.finished[finish.Lease.JobID] = finish
	return finish.Status, nil
}

func (s *store) ExtendLeases(context.Context, []jobs.Lease, time.Duration) ([]jobs.HeldLease, error) {
	return nil, nil
}
func (s *store) Rescue(context.Context, int) (int, error)                        { return 0, nil }
func (s *store) Sweep(context.Context, *jobs.SweepRequest) (int, error)          { return 0, nil }
func (s *store) EnqueueDueSchedule(context.Context, jobs.Schedule) (bool, error) { return false, nil }

func (s *store) status(id uuid.UUID) jobs.Status { return s.finished[id].Status }

// sealer is AES-GCM with a fixed key, the shape of a service's key ring.
type sealer struct{ aead cipher.AEAD }

func newSealer(t *testing.T) *sealer {
	t.Helper()
	block, err := aes.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	aead, err := cipher.NewGCM(block)
	require.NoError(t, err)
	return &sealer{aead: aead}
}

func (s *sealer) Seal(plaintext, associatedData []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	_, _ = rand.Read(nonce)
	return s.aead.Seal(nonce, nonce, plaintext, associatedData), nil
}

func (s *sealer) Open(sealed, associatedData []byte) ([]byte, error) {
	size := s.aead.NonceSize()
	if len(sealed) < size {
		return nil, errors.New("short")
	}
	return s.aead.Open(nil, sealed[:size], sealed[size:], associatedData)
}

// templateFiles hold a reset mail with a bearer link and a receipt with a large number.
var templateFiles = fstest.MapFS{
	"reset.en.html":   {Data: []byte(`<p>Hello {{.Name}}</p><a href="{{.ResetURL}}">reset</a>`)},
	"reset.en.txt":    {Data: []byte(`{{define "subject"}}Reset your password{{end}}Reset: {{.ResetURL}}`)},
	"reset.de.html":   {Data: []byte(`<p>Hallo {{.Name}}</p><a href="{{.ResetURL}}">neu</a>`)},
	"reset.de.txt":    {Data: []byte(`{{define "subject"}}Passwort neu setzen{{end}}Neu: {{.ResetURL}}`)},
	"receipt.en.html": {Data: []byte(`<p>Order {{.Order}}</p>`)},
	"receipt.en.txt":  {Data: []byte(`{{define "subject"}}Order {{.Order}}{{end}}Order {{.Order}}`)},
}

type resetData struct {
	Name     string
	ResetURL string
}

const resetURL = "https://tasks.example.com/reset#token=bearer-secret"

func newTemplates(t *testing.T, files fstest.MapFS) *mail.Templates {
	t.Helper()
	templates, err := mail.NewTemplates(&mail.TemplatesOptions{FS: files, DefaultLocale: "en"})
	require.NoError(t, err)
	return templates
}

func newSender(t *testing.T, options *jobsadapter.Options) *jobsadapter.Sender {
	t.Helper()
	if options.Templates == nil {
		options.Templates = newTemplates(t, templateFiles)
	}
	sender, err := jobsadapter.New(options)
	require.NoError(t, err)
	return sender
}

func newWorker(t *testing.T, sender *jobsadapter.Sender) (*jobs.Worker, *store) {
	t.Helper()
	jobStore := &store{finished: map[uuid.UUID]*jobs.Finish{}, payloads: map[uuid.UUID][]byte{}}
	worker, err := jobs.NewWorker(&jobs.WorkerOptions{Store: jobStore, Queues: map[string]jobs.QueueOptions{"mail": {}}})
	require.NoError(t, err)
	require.NoError(t, sender.Handle(worker, jobsadapter.Kind))
	return worker, jobStore
}

func envelope() *mail.Message {
	return &mail.Message{From: "noreply@example.com", To: []string{"ada@example.com"}}
}

func message() *mail.Message {
	return &mail.Message{
		From: "noreply@example.com", To: []string{"ada@example.com"}, Subject: "Hi", Text: "x",
		Attachments: []mail.Attachment{{Filename: "a.txt", Content: []byte("attached")}},
	}
}

func TestATemplateMailIsRenderedWhenTheJobRunsAndThePayloadHoldsNoRenderedMail(t *testing.T) {
	recorder := &mailtest.Recorder{}
	sender := newSender(t, &jobsadapter.Options{Transport: recorder})
	worker, jobStore := newWorker(t, sender)
	recipient := envelope()
	recipient.Subject, recipient.HTML = "ignored", "<p>ignored</p>"

	payload, err := sender.Template("reset", "de-AT", &resetData{Name: "Ada", ResetURL: resetURL}, recipient)
	require.NoError(t, err)
	id := jobStore.enqueue(t, payload)

	var stored map[string]any
	require.NoError(t, json.Unmarshal(jobStore.payloads[id], &stored))
	require.Equal(t, map[string]any{
		"template": "reset", "locale": "de-AT",
		"data":    map[string]any{"Name": "Ada", "ResetURL": resetURL},
		"message": map[string]any{"from": "noreply@example.com", "to": []any{"ada@example.com"}, "subject": ""},
	}, stored, "a reference: template, locale, envelope and data, no rendered subject or body")

	require.Equal(t, 1, jobstest.Drain(t, worker))

	require.Equal(t, jobs.StatusCompleted, jobStore.status(id))
	sent := recorder.Messages()
	require.Len(t, sent, 1)
	require.Equal(t, "Passwort neu setzen", sent[0].Subject, "rendered in the payload's locale, falling back from de-AT")
	require.Contains(t, sent[0].HTML, `href="`+resetURL+`"`)
	require.Equal(t, "Neu: "+resetURL+"\n", sent[0].Text)
	require.Equal(t, []string{"ada@example.com"}, sent[0].To)
	require.Equal(t, id.String(), sent[0].IdempotencyKey)
}

func TestSealedTemplateDataIsNotInThePayloadAndOpensOnlyForItsRecipients(t *testing.T) {
	recorder := &mailtest.Recorder{}
	sender := newSender(t, &jobsadapter.Options{Transport: recorder, Sealer: newSealer(t)})
	worker, jobStore := newWorker(t, sender)

	payload, err := sender.Template("reset", "en", &resetData{Name: "Ada", ResetURL: resetURL}, envelope())
	require.NoError(t, err)
	require.Nil(t, payload.Data)
	sent := jobStore.enqueue(t, payload)
	require.NotContains(t, string(jobStore.payloads[sent]), "bearer-secret")
	require.NotContains(t, string(jobStore.payloads[sent]), "Ada\"", "the whole data is sealed, the name too")

	moved := payload
	moved.Message.To = []string{"mallory@example.com"}
	refused := jobStore.enqueue(t, moved)

	require.Equal(t, 2, jobstest.Drain(t, worker))

	require.Equal(t, jobs.StatusCompleted, jobStore.status(sent))
	require.Contains(t, recorder.Messages()[0].HTML, resetURL)
	require.Equal(t, jobs.StatusPending, jobStore.status(refused), "data moved to another recipient does not open; it retries like a missing key")
	require.Len(t, recorder.Messages(), 1)
}

func TestTemplateRendersAtEnqueueSoABrokenMailFailsInTheCallersTransaction(t *testing.T) {
	sender := newSender(t, &jobsadapter.Options{Transport: &mailtest.Recorder{}})

	_, err := sender.Template("missing", "en", nil, envelope())
	require.ErrorIs(t, err, mail.ErrTemplateNotFound)

	_, err = sender.Template("reset", "en", map[string]string{"Name": "Ada"}, envelope())
	require.ErrorContains(t, err, "ResetURL", "a missing key fails now, not in the job")

	_, err = sender.Template("reset", "en", &resetData{Name: "Ada", ResetURL: "javascript:alert(1)"}, envelope())
	require.ErrorIs(t, err, mail.ErrUnsafeLink)

	_, err = sender.Template("reset", "en", &resetData{ResetURL: resetURL}, &mail.Message{})
	require.ErrorContains(t, err, "recipient")

	_, err = sender.Template("reset", "en", func() {}, envelope())
	require.ErrorContains(t, err, "encode")

	plain, err := jobsadapter.New(&jobsadapter.Options{Transport: &mailtest.Recorder{}})
	require.NoError(t, err)
	_, err = plain.Template("reset", "en", &resetData{ResetURL: resetURL}, envelope())
	require.ErrorContains(t, err, "Options.Templates")
}

func TestNumbersRenderWithEveryDigit(t *testing.T) {
	recorder := &mailtest.Recorder{}
	sender := newSender(t, &jobsadapter.Options{Transport: recorder})
	worker, jobStore := newWorker(t, sender)

	payload, err := sender.Template("receipt", "en", map[string]uint64{"Order": 18446744073709551615}, envelope())
	require.NoError(t, err)
	jobStore.enqueue(t, payload)
	jobstest.Drain(t, worker)

	require.Equal(t, "Order 18446744073709551615", recorder.Messages()[0].Subject)
}

// resetReference is the data of a reset mail that references the secret instead of carrying it.
type resetReference struct {
	Name      string
	RequestID string
}

func TestResolveTurnsAReferenceIntoTemplateDataWhenTheJobRuns(t *testing.T) {
	recorder := &mailtest.Recorder{}
	tokens := map[string]string{"request-1": "bearer-secret"} // the application's reset requests
	resolveReset := func(_ context.Context, data json.RawMessage) (any, error) {
		var reference resetReference
		if err := json.Unmarshal(data, &reference); err != nil {
			return nil, jobs.Permanent(err)
		}
		token, ok := tokens[reference.RequestID]
		if !ok {
			return nil, jobs.Permanent(errors.New("the reset request was used or revoked"))
		}
		return &resetData{Name: reference.Name, ResetURL: "https://tasks.example.com/reset#token=" + token}, nil
	}
	sender := newSender(t, &jobsadapter.Options{Transport: recorder, Resolve: map[string]jobsadapter.ResolveFunc{"reset": resolveReset}})
	worker, jobStore := newWorker(t, sender)

	payload, err := sender.Template("reset", "en", &resetReference{Name: "Ada", RequestID: "request-1"}, envelope())
	require.NoError(t, err, "with a ResolveFunc the stored data is a reference, so Template does not render it")
	sent := jobStore.enqueue(t, payload)
	require.NotContains(t, string(jobStore.payloads[sent]), "bearer-secret")
	payload, err = sender.Template("reset", "en", &resetReference{Name: "Ada", RequestID: "request-2"}, envelope())
	require.NoError(t, err)
	revoked := jobStore.enqueue(t, payload)

	jobstest.Drain(t, worker)

	require.Equal(t, jobs.StatusCompleted, jobStore.status(sent))
	require.Contains(t, recorder.Messages()[0].HTML, resetURL)
	require.Equal(t, jobs.StatusDead, jobStore.status(revoked))
	require.Len(t, recorder.Messages(), 1)
}

func TestResolveErrorsRetryUnlessPermanent(t *testing.T) {
	for _, testCase := range []struct {
		err    error
		status jobs.Status
	}{
		{err: errors.New("database unavailable"), status: jobs.StatusPending},
		{err: fmt.Errorf("%w: unknown request", mail.ErrPermanent), status: jobs.StatusDead},
	} {
		sender := newSender(t, &jobsadapter.Options{Transport: &mailtest.Recorder{}, Resolve: map[string]jobsadapter.ResolveFunc{
			"reset": func(context.Context, json.RawMessage) (any, error) { return nil, testCase.err },
		}})
		worker, jobStore := newWorker(t, sender)
		payload, err := sender.Template("reset", "en", nil, envelope())
		require.NoError(t, err)
		id := jobStore.enqueue(t, payload)

		jobstest.Drain(t, worker)

		require.Equal(t, testCase.status, jobStore.status(id), testCase.err.Error())
	}
}

// A ResolveFunc is per template: the other templates still render at enqueue, and every template
// name is checked there.
func TestTemplatesWithoutAResolveFuncStillRenderAtEnqueue(t *testing.T) {
	sender := newSender(t, &jobsadapter.Options{Transport: &mailtest.Recorder{}, Resolve: map[string]jobsadapter.ResolveFunc{
		"reset": func(context.Context, json.RawMessage) (any, error) { return &resetData{ResetURL: resetURL}, nil },
	}})

	_, err := sender.Template("reset", "en", &resetReference{RequestID: "request-1"}, envelope())
	require.NoError(t, err)
	_, err = sender.Template("receipt", "en", map[string]string{}, envelope())
	require.ErrorContains(t, err, "Order", "a template without a ResolveFunc renders at enqueue")
	_, err = sender.Template("recipt", "en", map[string]string{"Order": "1"}, envelope())
	require.ErrorIs(t, err, mail.ErrTemplateNotFound, "a typo fails in the caller's transaction, not after ten attempts")
}

// The handler's error text is stored as the job's last error and shown in the admin listing, so a
// render failure must not carry the bearer link: neither one Resolve returns nor one from sealed data.
func TestARenderFailureInTheJobStoresNoSecretInTheLastError(t *testing.T) {
	relative := newSender(t, &jobsadapter.Options{Transport: &mailtest.Recorder{}, Resolve: map[string]jobsadapter.ResolveFunc{
		"reset": func(context.Context, json.RawMessage) (any, error) {
			return &resetData{ResetURL: "/reset#token=bearer-secret"}, nil // the public base URL is missing in configuration
		},
	}})
	worker, jobStore := newWorker(t, relative)
	payload, err := relative.Template("reset", "en", &resetReference{RequestID: "request-1"}, envelope())
	require.NoError(t, err)
	resolved := jobStore.enqueue(t, payload)
	jobstest.Drain(t, worker)

	sealer := newSealer(t)
	enqueuing := newSender(t, &jobsadapter.Options{Transport: &mailtest.Recorder{}, Sealer: sealer})
	payload, err = enqueuing.Template("reset", "en", &resetData{ResetURL: resetURL}, envelope())
	require.NoError(t, err)
	deployed := maps.Clone(templateFiles) // a template deployed after the enqueue breaks the link
	deployed["reset.en.html"] = &fstest.MapFile{Data: []byte(`<a href="x{{.ResetURL}}">reset</a>`)}
	sealedWorker, sealedStore := newWorker(t, newSender(t, &jobsadapter.Options{Transport: &mailtest.Recorder{}, Sealer: sealer, Templates: newTemplates(t, deployed)}))
	sealed := sealedStore.enqueue(t, payload)
	jobstest.Drain(t, sealedWorker)

	for _, finish := range []*jobs.Finish{jobStore.finished[resolved], sealedStore.finished[sealed]} {
		require.Equal(t, jobs.StatusDead, finish.Status)
		require.Contains(t, finish.Error, "unsafe link")
		require.NotContains(t, finish.Error, "bearer-secret")
	}

	// text/template's reason for an action that does not fit the data prints the value.
	deployed["reset.en.html"] = &fstest.MapFile{Data: []byte(`{{range .ResetURL}}{{.}}{{end}}`)}
	rangeWorker, rangeStore := newWorker(t, newSender(t, &jobsadapter.Options{Transport: &mailtest.Recorder{}, Sealer: sealer, Templates: newTemplates(t, deployed)}))
	ranged := rangeStore.enqueue(t, payload)
	jobstest.Drain(t, rangeWorker)
	require.Equal(t, jobs.StatusDead, rangeStore.finished[ranged].Status)
	require.Contains(t, rangeStore.finished[ranged].Error, "at <.ResetURL>")
	require.NotContains(t, rangeStore.finished[ranged].Error, "bearer-secret")
}

func TestTheQueuedPayloadDoesNotShareTheEnvelopeWithTheCaller(t *testing.T) {
	sender := newSender(t, &jobsadapter.Options{Transport: &mailtest.Recorder{}})
	reused := envelope()
	reused.Headers = map[string]string{"X-Campaign": "invite"}
	reused.Attachments = []mail.Attachment{{Filename: "a.txt", Content: []byte("a")}}

	payload, err := sender.Template("reset", "en", &resetData{ResetURL: resetURL}, reused)
	require.NoError(t, err)
	reused.To[0], reused.Headers["X-Campaign"], reused.Attachments[0].Filename = "bob@example.com", "other", "b.txt"

	require.Equal(t, []string{"ada@example.com"}, payload.Message.To)
	require.Equal(t, "invite", payload.Message.Headers["X-Campaign"])
	require.Equal(t, "a.txt", payload.Message.Attachments[0].Filename)
}

// A worker that cannot render yet retries: during a rolling deploy an older replica may claim a
// job of a template only the newer one has, or one without the sealing key. Data that does not
// render is dead at once.
func TestAWorkerThatCannotRenderYetRetriesAndDataThatDoesNotRenderIsDead(t *testing.T) {
	older := fstest.MapFS{"receipt.en.html": templateFiles["receipt.en.html"], "receipt.en.txt": templateFiles["receipt.en.txt"]}
	sealed := newSender(t, &jobsadapter.Options{Transport: &mailtest.Recorder{}, Sealer: newSealer(t)})
	sealedPayload, err := sealed.Template("reset", "en", &resetData{ResetURL: resetURL}, envelope())
	require.NoError(t, err)

	recorder := &mailtest.Recorder{}
	worker, jobStore := newWorker(t, newSender(t, &jobsadapter.Options{Transport: recorder, Templates: newTemplates(t, older)}))
	unknown := jobStore.enqueue(t, jobsadapter.Payload{Template: "reset", Data: json.RawMessage(`{}`), Message: *envelope()})
	noSealer := jobStore.enqueue(t, jobsadapter.Payload{Template: "receipt", SealedData: sealedPayload.SealedData, Message: *envelope()})
	missingKey := jobStore.enqueue(t, jobsadapter.Payload{Template: "receipt", Data: json.RawMessage(`{}`), Message: *envelope()})
	empty := jobStore.enqueue(t, jobsadapter.Payload{Template: "receipt", Message: *envelope()})

	jobstest.Drain(t, worker)

	require.Equal(t, jobs.StatusPending, jobStore.status(unknown), "a template this worker does not know")
	require.Equal(t, jobs.StatusPending, jobStore.status(noSealer), "sealed data without a Sealer")
	require.Equal(t, jobs.StatusDead, jobStore.status(missingKey), "data without a key the template needs")
	require.Equal(t, jobs.StatusDead, jobStore.status(empty), "no data renders as nil, which misses the key")
	require.Empty(t, recorder.Messages())

	noTemplates, err := jobsadapter.New(&jobsadapter.Options{Transport: recorder})
	require.NoError(t, err)
	worker, jobStore = newWorker(t, noTemplates)
	id := jobStore.enqueue(t, jobsadapter.Payload{Template: "receipt", Data: json.RawMessage(`{"Order":1}`), Message: *envelope()})
	jobstest.Drain(t, worker)
	require.Equal(t, jobs.StatusPending, jobStore.status(id), "a worker without Templates")
}

func TestAPreRenderedMessageIsSentAsItIsWithTheJobIDAsIdempotencyKey(t *testing.T) {
	recorder := &mailtest.Recorder{}
	worker, jobStore := newWorker(t, newSender(t, &jobsadapter.Options{Transport: recorder}))
	id := jobStore.enqueue(t, jobsadapter.Payload{Message: *message()})

	require.Equal(t, 1, jobstest.Drain(t, worker))

	require.Equal(t, jobs.StatusCompleted, jobStore.status(id))
	sent := recorder.Messages()
	require.Len(t, sent, 1)
	require.Equal(t, "Hi", sent[0].Subject)
	require.Equal(t, id.String(), sent[0].IdempotencyKey)
	require.Equal(t, []byte("attached"), sent[0].Attachments[0].Content, "the payload round-trips through JSON")
}

func TestAMessagesOwnIdempotencyKeyWins(t *testing.T) {
	recorder := &mailtest.Recorder{}
	sender := newSender(t, &jobsadapter.Options{Transport: recorder})
	worker, jobStore := newWorker(t, sender)
	own := message()
	own.IdempotencyKey = "welcome/42"
	jobStore.enqueue(t, jobsadapter.Payload{Message: *own})
	keyed := envelope()
	keyed.IdempotencyKey = "reset/7"
	payload, err := sender.Template("reset", "en", &resetData{ResetURL: resetURL}, keyed)
	require.NoError(t, err)
	jobStore.enqueue(t, payload)

	jobstest.Drain(t, worker)

	keys := []string{recorder.Messages()[0].IdempotencyKey, recorder.Messages()[1].IdempotencyKey}
	require.ElementsMatch(t, []string{"welcome/42", "reset/7"}, keys)
}

func TestAPermanentFailureIsDeadAndATemporaryOneRetries(t *testing.T) {
	for _, testCase := range []struct {
		err    error
		status jobs.Status
	}{
		{err: fmt.Errorf("%w: 550 no such user", mail.ErrPermanent), status: jobs.StatusDead},
		{err: errors.New("connection reset"), status: jobs.StatusPending},
	} {
		recorder := &mailtest.Recorder{}
		recorder.FailWith(testCase.err)
		worker, jobStore := newWorker(t, newSender(t, &jobsadapter.Options{Transport: recorder}))
		id := jobStore.enqueue(t, jobsadapter.Payload{Message: *message()})

		jobstest.Drain(t, worker)

		require.Equal(t, testCase.status, jobStore.status(id), testCase.err.Error())
	}
}

func TestTheMailersValidationMakesABrokenMessageDead(t *testing.T) {
	mailer, err := mail.New(&mail.Options{Transport: &mailtest.Recorder{}})
	require.NoError(t, err)
	worker, jobStore := newWorker(t, newSender(t, &jobsadapter.Options{Transport: mailer}))
	broken := message()
	broken.To = nil
	id := jobStore.enqueue(t, jobsadapter.Payload{Message: *broken})

	jobstest.Drain(t, worker)

	require.Equal(t, jobs.StatusDead, jobStore.status(id))
}

func TestNewNeedsATransport(t *testing.T) {
	_, err := jobsadapter.New(nil)
	require.Error(t, err)
	_, err = jobsadapter.New(&jobsadapter.Options{})
	require.Error(t, err)
}
