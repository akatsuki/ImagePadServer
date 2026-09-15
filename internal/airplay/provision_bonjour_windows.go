//go:build windows

package airplay

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const bonjourServiceName = "Bonjour Service"

func ensureBonjour(ctx context.Context, root string) error {
	installed, running, detail := queryBonjourService(ctx)
	if installed && running {
		return nil
	}
	if installed {
		if err := startBonjourService(ctx); err == nil {
			if waitForBonjour(ctx) == nil {
				return nil
			}
		} else {
			detail = appendDetail(detail, err.Error())
		}
	}

	installer := filepath.Join(root, "mDNSResponder.exe")
	if err := runBonjourInstaller(ctx, installer); err != nil {
		detail = appendDetail(detail, err.Error())
		if elevatedErr := runBonjourInstallerElevated(ctx, installer); elevatedErr != nil {
			detail = appendDetail(detail, elevatedErr.Error())
		}
	}
	if err := waitForBonjour(ctx); err != nil {
		return fmt.Errorf("Bonjour Service is unavailable after automatic installation: %s", appendDetail(detail, err.Error()))
	}
	return nil
}

func queryBonjourService(ctx context.Context) (installed, running bool, detail string) {
	cmd := exec.CommandContext(ctx, "sc.exe", "query", bonjourServiceName)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	text := strings.TrimSpace(out.String())
	if err != nil {
		return false, false, text
	}
	return true, strings.Contains(strings.ToUpper(text), "RUNNING"), text
}

func startBonjourService(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "sc.exe", "start", bonjourServiceName)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("starting Bonjour Service: %w (%s)", err, strings.TrimSpace(out.String()))
	}
	return nil
}

func waitForBonjour(ctx context.Context) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		installed, running, detail := queryBonjourService(ctx)
		if installed && running {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if detail == "" {
				continue
			}
		}
	}
}

func runBonjourInstaller(ctx context.Context, installer string) error {
	cmd := exec.CommandContext(ctx, installer, "-install")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running %s -install: %w (%s)", installer, err, strings.TrimSpace(out.String()))
	}
	return nil
}

func runBonjourInstallerElevated(ctx context.Context, installer string) error {
	quote := string(rune(39))
	escaped := strings.ReplaceAll(installer, quote, quote+quote)
	script := "$p = Start-Process -FilePath " + quote + escaped + quote + " -ArgumentList " + quote + "-install" + quote + " -Verb RunAs -Wait -PassThru; exit $p.ExitCode"
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", script)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("elevated Bonjour installation (UAC approval may be required): %w (%s)", err, strings.TrimSpace(out.String()))
	}
	return nil
}

func appendDetail(current, extra string) string {
	if strings.TrimSpace(extra) == "" {
		return current
	}
	if strings.TrimSpace(current) == "" {
		return strings.TrimSpace(extra)
	}
	return strings.TrimSpace(current + "; " + extra)
}
