//go:build windows
// +build windows

package tray

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unicode/utf16"

	"imagepadserver/internal/appicon"
)

const pipeName = "ImagePadServer.Tray"

// Tray represents the optional Windows notification-area icon.
type Tray struct {
	cmd *exec.Cmd
}

// Start shows a Windows notification-area icon that opens serverURL when clicked.
func Start(serverURL string) (*Tray, error) {
	iconPath, _ := ensureTrayIcon()
	cmd := exec.Command(
		"powershell",
		"-NoProfile",
		"-NonInteractive",
		"-STA",
		"-ExecutionPolicy",
		"Bypass",
		"-EncodedCommand",
		encodePowerShell(trayScript(serverURL, os.Getpid(), iconPath)),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Tray{cmd: cmd}, nil
}

// Stop removes the icon by stopping its helper process.
func (t *Tray) Stop() {
	if t == nil || t.cmd == nil || t.cmd.Process == nil {
		return
	}
	_ = signalExistingTray()
	done := make(chan struct{})
	go func() {
		_, _ = t.cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(1500 * time.Millisecond):
	}
	_ = t.cmd.Process.Kill()
	<-done
}

func encodePowerShell(script string) string {
	encoded := utf16.Encode([]rune(script))
	data := make([]byte, len(encoded)*2)
	for i, v := range encoded {
		binary.LittleEndian.PutUint16(data[i*2:], v)
	}
	return base64.StdEncoding.EncodeToString(data)
}

func ensureTrayIcon() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "ImagePadServer")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "imagepad-tray.ico")
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, appicon.IconICO) {
		return path, nil
	}
	if err := os.WriteFile(path, appicon.IconICO, 0644); err != nil {
		return "", err
	}
	return path, nil
}

func trayScript(serverURL string, parentPID int, iconPath string) string {
	return `
$ErrorActionPreference = 'SilentlyContinue'
$url = '` + escapeSingleQuoted(serverURL) + `'
$iconPath = '` + escapeSingleQuoted(iconPath) + `'
$pipeName = 'ImagePadServer.Tray'
$parentPID = ` + stringInt(parentPID) + `
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
Add-Type -AssemblyName System.IO.Pipes

try {
  $client = New-Object System.IO.Pipes.NamedPipeClientStream('.', $pipeName, [System.IO.Pipes.PipeDirection]::Out)
  $client.Connect(100)
  $writer = New-Object System.IO.StreamWriter($client)
  $writer.AutoFlush = $true
  $writer.WriteLine('exit')
  $writer.Dispose()
  $client.Dispose()
  Start-Sleep -Milliseconds 350
} catch {}

$notify = New-Object System.Windows.Forms.NotifyIcon
if ($iconPath -and (Test-Path -LiteralPath $iconPath)) {
  $appIcon = New-Object System.Drawing.Icon($iconPath)
}
if ($null -eq $appIcon) { $appIcon = [System.Drawing.SystemIcons]::Application }
$notify.Icon = $appIcon
$notify.Text = 'ImagePadServer'
$notify.Visible = $true

$menu = New-Object System.Windows.Forms.ContextMenuStrip
$openItem = $menu.Items.Add('ブラウザを開く')
$openItem.add_Click({ Start-Process $url })
$closeItem = $menu.Items.Add('アイコンを閉じる')
$closeItem.add_Click({
  $notify.Visible = $false
  [System.Windows.Forms.Application]::Exit()
})
$notify.ContextMenuStrip = $menu

$notify.add_MouseClick({
  param($sender, $eventArgs)
  if ($eventArgs.Button -eq [System.Windows.Forms.MouseButtons]::Left) {
    Start-Process $url
  }
})

$timer = New-Object System.Windows.Forms.Timer
$timer.Interval = 1000
$timer.add_Tick({
  if ($null -ne $script:server) { return }
  $script:server = New-Object System.IO.Pipes.NamedPipeServerStream($pipeName, [System.IO.Pipes.PipeDirection]::In, 1, [System.IO.Pipes.PipeTransmissionMode]::Message, [System.IO.Pipes.PipeOptions]::Asynchronous)
  $script:async = $script:server.BeginWaitForConnection($null, $null)
})
$timer.Start()

try {
  $script:server = New-Object System.IO.Pipes.NamedPipeServerStream($pipeName, [System.IO.Pipes.PipeDirection]::In, 1, [System.IO.Pipes.PipeTransmissionMode]::Message, [System.IO.Pipes.PipeOptions]::Asynchronous)
  $script:async = $script:server.BeginWaitForConnection($null, $null)
  while ($true) {
    [System.Windows.Forms.Application]::DoEvents()
    Start-Sleep -Milliseconds 100
    if (-not (Get-Process -Id $parentPID -ErrorAction SilentlyContinue)) {
      break
    }
    if ($script:server -and $script:async -and $script:async.IsCompleted) {
      $script:server.EndWaitForConnection($script:async)
      break
    }
  }
} finally {
  $timer.Stop()
  $timer.Dispose()
  if ($script:server) { $script:server.Dispose() }
  $notify.Visible = $false
  $notify.Dispose()
  if ($appIcon -and $appIcon -ne [System.Drawing.SystemIcons]::Application) { $appIcon.Dispose() }
}
`
}

func signalExistingTray() error {
	cmd := exec.Command(
		"powershell",
		"-NoProfile",
		"-NonInteractive",
		"-STA",
		"-ExecutionPolicy",
		"Bypass",
		"-Command",
		"$ErrorActionPreference='Stop'; Add-Type -AssemblyName System.IO.Pipes; $client=New-Object System.IO.Pipes.NamedPipeClientStream('.', '"+pipeName+"', [System.IO.Pipes.PipeDirection]::Out); $client.Connect(200); $writer=New-Object System.IO.StreamWriter($client); $writer.AutoFlush=$true; $writer.WriteLine('exit'); $writer.Dispose(); $client.Dispose()",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

func escapeSingleQuoted(value string) string {
	out := ""
	for _, r := range value {
		if r == '\'' {
			out += "''"
			continue
		}
		out += string(r)
	}
	return out
}

func stringInt(value int) string {
	return fmt.Sprintf("%d", value)
}
