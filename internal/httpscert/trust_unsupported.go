//go:build !windows

package httpscert

func trustCertificate(string) bool {
	return false
}
