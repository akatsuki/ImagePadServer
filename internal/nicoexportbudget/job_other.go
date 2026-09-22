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
