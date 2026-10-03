//go:build !windows && !darwin && !linux

package voicevoxruntime

import (
	"context"
	"errors"
)

func launch(context.Context, string, string) (ownedProcess, error) {
	return nil, errors.New("VOICEVOX runtime launch unsupported")
}
