//go:build windows

package httpscert

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

func trustCertificate(certPath string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile",
		"-NonInteractive",
		"-WindowStyle", "Hidden",
		"-Command",
		"Import-Certificate -FilePath $args[0] -CertStoreLocation Cert:\\CurrentUser\\Root | Out-Null",
		certPath,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run() == nil
}
