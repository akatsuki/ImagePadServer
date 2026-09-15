package airplay

import "imagepadserver/internal/airplaycontract"

type PublisherExitDecision struct {
	Restart bool
	Reason  string
}

type sourceClockTerminationCause string

const (
	sourceClockTerminationNone             sourceClockTerminationCause = "none"
	sourceClockTerminationUserStop         sourceClockTerminationCause = "user-stop"
	sourceClockTerminationReceiverExit     sourceClockTerminationCause = "receiver-exit"
	sourceClockTerminationTimeout          sourceClockTerminationCause = "timeout"
	sourceClockTerminationPublisherFailure sourceClockTerminationCause = "publisher-failure"
)

func classifySourceClockExit(code int, crashed bool) PublisherExitDecision {
	if crashed {
		return PublisherExitDecision{Restart: true, Reason: "process-crash"}
	}

	switch code {
	case 0:
		return PublisherExitDecision{Reason: "stopped"}
	case 2:
		return PublisherExitDecision{Reason: "configuration-error"}
	case 20:
		return PublisherExitDecision{Reason: "no-signal"}
	case 21:
		return PublisherExitDecision{Reason: "protocol-error"}
	case 22:
		return PublisherExitDecision{Restart: true, Reason: "pipeline-error"}
	default:
		return PublisherExitDecision{Reason: "unclassified-exit"}
	}
}

func sourceClockPublisherExitAuthorizesSessionStop(int, bool, uint64, uint64, sourceClockTimeoutDecision) bool {
	// Publisher exits never own the receiver-session lifetime. A lifecycle
	// tick may independently decide that the receiver deadline has elapsed.
	return false
}

func sourceClockTerminationReason(decision sourceClockTimeoutDecision) string {
	switch decision {
	case sourceClockTimeoutNoInitialMedia:
		return airplaycontract.TerminationReasonNoInitialMedia
	case sourceClockTimeoutNoSignal:
		return airplaycontract.TerminationReasonNoSignalTimeout
	default:
		return ""
	}
}

func sourceClockTerminationCauseFor(userStop, receiverExit bool, timeoutDecision sourceClockTimeoutDecision, publisherExit bool) sourceClockTerminationCause {
	if userStop {
		return sourceClockTerminationUserStop
	}
	if receiverExit {
		return sourceClockTerminationReceiverExit
	}
	if timeoutDecision == sourceClockTimeoutNoInitialMedia || timeoutDecision == sourceClockTimeoutNoSignal {
		return sourceClockTerminationTimeout
	}
	if publisherExit {
		return sourceClockTerminationPublisherFailure
	}
	return sourceClockTerminationNone
}
