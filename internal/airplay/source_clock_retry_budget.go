package airplay

import "imagepadserver/internal/airplaycontract"

const (
	sourceClockRetryMaxAttempts = airplaycontract.RecoveryMaxAttempts
	sourceClockRetryWindow      = airplaycontract.RecoveryWindow
)

type sourceClockRetryPermission = airplaycontract.RecoveryPermission
type sourceClockRetryBudget = airplaycontract.RecoveryBudget
