package airplaycontract

import "context"

// managed=true without an owner is an incomplete managed session: fail closed,
// never fall back to the legacy independent generation allocator.
type ManagedRecoveryObserver interface {
	ManagedRecoveryOwner() (PublisherRecoveryOwner, bool)
}

type PublisherRecoveryExecutor interface {
	RecoverSourceClockDelivery(context.Context, CandidateDelivery) error
}

type PublisherRecoveryOwner interface {
	RecoverPublisher(context.Context, PublisherArtifacts, PublisherRecoveryExecutor) error
	PublisherHealthy(PublisherArtifacts)
}
