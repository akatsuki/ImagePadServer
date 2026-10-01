//go:build !windows

package nicoexportbudget

import (
	"context"
	"errors"
)

var ErrUnsupported = errors.New("nico export CPU budget requires Windows Job Objects")

func Run(ctx context.Context, spec ProcessSpec, options Options) (Report, error) {
	return Report{Reason: ErrUnsupported.Error()}, ErrUnsupported
}

// StartSession preserves the cross-platform API while failing closed where Job Objects are unavailable.
func StartSession(ctx context.Context, spec ProcessSpec, options Options) (*SessionProcess, error) {
	return nil, ErrUnsupported
}
