//go:build windows
// +build windows

package tray

import (
	"encoding/base64"
	"encoding/binary"
	"os/exec"
	"syscall"
	"unicode/utf16"
)

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
		encodePowerShell(trayScript(serverURL)),
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
	_ = t.cmd.Process.Kill()
	_, _ = t.cmd.Process.Wait()
}

func encodePowerShell(script string) string {
	encoded := utf16.Encode([]rune(script))
	data := make([]byte, len(encoded)*2)
	for i, v := range encoded {
		binary.LittleEndian.PutUint16(data[i*2:], v)
	}
	return base64.StdEncoding.EncodeToString(data)
}

func trayScript(serverURL string) string {
	return `
$ErrorActionPreference = 'SilentlyContinue'
$url = '` + escapeSingleQuoted(serverURL) + `'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing

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

[System.Windows.Forms.Application]::Run()
$notify.Visible = $false
$notify.Dispose()
`
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
