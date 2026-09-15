package airplay

import "sync"

const (
	runtimePreparationUnknown   = "unknown"
	runtimePreparationPreparing = "preparing"
	runtimePreparationReady     = "ready"
	runtimePreparationFailed    = "failed"
)

// RuntimePreparationStatus is deliberately small and cheap to read from the
// HTTP status path. It does not hash or inspect the runtime again.
type RuntimePreparationStatus struct {
	State        string
	Message      string
	RuntimeSetID string
}

var runtimePreparationState = struct {
	sync.RWMutex
	status RuntimePreparationStatus
}{status: RuntimePreparationStatus{State: runtimePreparationUnknown}}

func RuntimePreparation() RuntimePreparationStatus {
	runtimePreparationState.RLock()
	defer runtimePreparationState.RUnlock()
	return runtimePreparationState.status
}

func setRuntimePreparation(status RuntimePreparationStatus) {
	runtimePreparationState.Lock()
	runtimePreparationState.status = status
	runtimePreparationState.Unlock()
}
