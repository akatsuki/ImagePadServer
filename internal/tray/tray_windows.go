//go:build windows
// +build windows

package tray

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unicode/utf16"
)

const pipeName = "ImagePadServer.Tray"

// Tray represents the optional Windows notification-area icon.
type Tray struct {
	cmd *exec.Cmd
}

// Start shows a Windows notification-area icon that opens serverURL when clicked.
func Start(serverURL string) (*Tray, error) {
	cmd := exec.Command(
		"powershell",
		"-NoProfile",
		"-NonInteractive",
		"-STA",
		"-ExecutionPolicy",
		"Bypass",
		"-EncodedCommand",
		encodePowerShell(trayScript(serverURL, os.Getpid())),
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

func trayScript(serverURL string, parentPID int) string {
	return `
$ErrorActionPreference = 'SilentlyContinue'
$url = '` + escapeSingleQuoted(serverURL) + `'
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
$notify.Icon = [System.Drawing.SystemIcons]::Application
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
