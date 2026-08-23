//go:build !windows

package airplay

import "context"

func ensureBonjour(context.Context, string) error {
	return nil
}
