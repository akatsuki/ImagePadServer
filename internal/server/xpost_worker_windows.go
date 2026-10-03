//go:build windows

package server

import (
	"context"
	"errors"
	"imagepadserver/internal/nicoexportbudget"
)

func runXPostPortable(context.Context, nicoexportbudget.ProcessSpec) error {
	return errors.New("use Windows Job Object for X export")
}
