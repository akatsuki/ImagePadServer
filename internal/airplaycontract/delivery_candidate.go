package airplaycontract

import (
	"context"
	"errors"
	"time"
)

var ErrCandidatePublisherExitUnconfirmed = errors.New("candidate publisher exit is unconfirmed")

// CandidateDeliveryBinding is a session-owner-issued, immutable handoff to
// the publisher owner. It is never created from an HTTP request body.
type CandidateDeliveryBinding struct {
	ExpectedActiveGeneration uint64
	Descriptor               DeliveryGeneration
	PublishURL               string
}

// CandidateDelivery separates slow output validation/drain from the final
// commit. Validate must honor cancellation and MUST NOT promote a route. The
// publisher owner rechecks receiver exit, its process and the session deadline
// before calling Commit. Abort is called only after publisher cleanup is known.
// Commit must perform no process waits or callbacks into the publisher owner;
// it can be called while that owner's session state lock fences user stop.
// ClaimPublisher precedes process startup. After a claim, backend Abort must
// require an explicit not-started or exit-confirmed publisher completion.
type CandidateDelivery interface {
	Binding() CandidateDeliveryBinding
	ClaimPublisher() error
	Validate(context.Context, []Event, []Event) error
	Commit(time.Time) error
	Abort() error
}
