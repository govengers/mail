package mail

import "time"

// Observer receives every send of a Mailer (LIB-012); prometheusadapter.Metrics implements it.
// The Mailer calls it synchronously from the sending goroutine, so implementations must be safe
// for concurrent use and fast. The interface may gain methods in minor versions.
type Observer interface {
	SendFinished(outcome *Outcome)
}

// Result is how a send ended.
type Result string

// The results an Observer receives.
const (
	ResultSent     Result = "sent"     // the transport accepted the message
	ResultInvalid  Result = "invalid"  // the message failed Validate and was not sent
	ResultRejected Result = "rejected" // the transport refused it for good (ErrPermanent)
	ResultFailed   Result = "failed"   // any other error; sending again may succeed
)

// Outcome is one finished send.
type Outcome struct {
	Message  *Message // as sent, with the default From filled in; read only
	Result   Result
	Duration time.Duration // validation and transport
	Err      error         // nil when Result is ResultSent
}
