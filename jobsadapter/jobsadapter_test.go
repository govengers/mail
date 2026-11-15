package jobsadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
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
}

func (s *store) enqueue(t *testing.T, message *mail.Message) uuid.UUID {
	t.Helper()
	payload, err := jobsadapter.Kind.New(*message, nil).EncodePayload()
	require.NoError(t, err)
	id := uuid.New()
	s.pending = append(s.pending, &jobs.Claim{
		Lease: jobs.Lease{JobID: id, Token: uuid.New()}, Queue: jobsadapter.Kind.Queue(), Kind: jobsadapter.Kind.Name(),
		Payload: payload, Attempt: 1, MaximumAttempts: jobs.DefaultMaximumAttempts,
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

func newWorker(t *testing.T, transport mail.Transport) (*jobs.Worker, *store) {
	t.Helper()
	jobStore := &store{finished: map[uuid.UUID]*jobs.Finish{}}
	worker, err := jobs.NewWorker(&jobs.WorkerOptions{Store: jobStore, Queues: map[string]jobs.QueueOptions{"mail": {}}})
	require.NoError(t, err)
	require.NoError(t, jobsadapter.Handle(worker, jobsadapter.Kind, transport))
	return worker, jobStore
}

func message() *mail.Message {
	return &mail.Message{
		From: "noreply@example.com", To: []string{"ada@example.com"}, Subject: "Hi", Text: "x",
		Attachments: []mail.Attachment{{Filename: "a.txt", Content: []byte("attached")}},
	}
}

func TestAJobSendsItsMessageWithTheJobIDAsIdempotencyKey(t *testing.T) {
	recorder := &mailtest.Recorder{}
	worker, jobStore := newWorker(t, recorder)
	id := jobStore.enqueue(t, message())

	require.Equal(t, 1, jobstest.Drain(t, worker))

	require.Equal(t, jobs.StatusCompleted, jobStore.status(id))
	sent := recorder.Messages()
	require.Len(t, sent, 1)
	require.Equal(t, id.String(), sent[0].IdempotencyKey)
	require.Equal(t, []byte("attached"), sent[0].Attachments[0].Content, "the payload round-trips through JSON")
}

func TestAMessagesOwnIdempotencyKeyWins(t *testing.T) {
	recorder := &mailtest.Recorder{}
	worker, jobStore := newWorker(t, recorder)
	own := message()
	own.IdempotencyKey = "welcome/42"
	jobStore.enqueue(t, own)

	jobstest.Drain(t, worker)

	require.Equal(t, "welcome/42", recorder.Messages()[0].IdempotencyKey)
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
		worker, jobStore := newWorker(t, recorder)
		id := jobStore.enqueue(t, message())

		jobstest.Drain(t, worker)

		require.Equal(t, testCase.status, jobStore.status(id), testCase.err.Error())
	}
}

func TestTheMailersValidationMakesABrokenMessageDead(t *testing.T) {
	mailer, err := mail.New(&mail.Options{Transport: &mailtest.Recorder{}})
	require.NoError(t, err)
	worker, jobStore := newWorker(t, mailer)
	broken := message()
	broken.To = nil
	id := jobStore.enqueue(t, broken)

	jobstest.Drain(t, worker)

	require.Equal(t, jobs.StatusDead, jobStore.status(id))
}

func TestThePayloadIsTheMessagesJSON(t *testing.T) {
	payload, err := jobsadapter.Kind.New(mail.Message{To: []string{"a@example.com"}, Subject: "Hi"}, nil).EncodePayload()
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(payload, &fields))
	require.Equal(t, map[string]any{"to": []any{"a@example.com"}, "subject": "Hi"}, fields)
}

func TestHandleNeedsATransport(t *testing.T) {
	worker, err := jobs.NewWorker(&jobs.WorkerOptions{Store: &store{}})
	require.NoError(t, err)
	require.Error(t, jobsadapter.Handle(worker, jobsadapter.Kind, nil))
}
