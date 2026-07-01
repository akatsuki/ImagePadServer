# Public RTSP/TCP over UPnP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish the OBS real-time stream as a standard `rtsp://` URL over TCP, temporarily exposed through UPnP for PC and Android VRChat.

**Architecture:** `internal/upnp` returns an owned, idempotently closable TCP mapping. The OBS manager exposes the ready MediaMTX endpoint and accepts an advertised URL update, while `internal/server` owns mapping creation, CGNAT rejection, stale-session protection, and cleanup. The normal share URL UI displays RTSP instead of rendering a dedicated mode-specific block.

**Tech Stack:** Go 1.25, MediaMTX, RTSP/RTP interleaved over TCP, UPnP IGD SOAP, embedded HTML/CSS/JavaScript.

**Design:** `docs/superpowers/specs/2026-06-29-public-rtsp-tcp-upnp-design.md`

---

## File Structure

- Create `internal/upnp/upnp_test.go`: deterministic SOAP-server tests for owned mapping creation, deletion, idempotency, and public-address classification.
- Modify `internal/upnp/upnp.go`: expose an owned `TCPMapping`, support separate internal/external ports, and add globally-routable IPv4 validation.
- Modify `internal/obsrtmp/mediamtx.go`: generate standard `rtsp://` player URLs and permit external reads only on the randomized session path.
- Modify `internal/obsrtmp/mediamtx_test.go`: lock TCP-only configuration, external read permission, and URL scheme.
- Modify `internal/obsrtmp/manager.go`: publish an RTSP-ready endpoint callback and allow a session-checked advertised URL update.
- Modify `internal/obsrtmp/manager_test.go`: verify callback data, stale update rejection, status cleanup, and compatibility labels.
- Modify `internal/server/server.go`: own the active UPnP mapping, map only published RTSP sessions, reject CGNAT addresses, update the share URL, and clean up on every stop path.
- Modify `internal/server/server_test.go`: test successful mapping, fallback, stale callback protection, cleanup, and share URL selection with injectable mapping functions.
- Modify `internal/server/ui.go`: remove the dedicated RTSP block, use the existing share URL/copy surface, and require a non-persisted risk confirmation before selecting RTSP TCP.

---

### Task 1: Add an owned UPnP TCP mapping

**Files:**
- Create: `internal/upnp/upnp_test.go`
- Modify: `internal/upnp/upnp.go`

- [ ] **Step 1: Write failing public IPv4 classification tests**

Add table tests for `IsGloballyRoutableIPv4`:

```go
func TestIsGloballyRoutableIPv4(t *testing.T) {
	tests := map[string]bool{
		"8.8.8.8":       true,
		"127.0.0.1":     false,
		"10.0.0.1":      false,
		"172.16.0.1":    false,
		"192.168.1.1":   false,
		"100.64.0.1":    false,
		"169.254.1.1":   false,
		"224.0.0.1":     false,
		"2001:db8::1":   false,
		"not-an-address": false,
	}
	for input, want := range tests {
		if got := IsGloballyRoutableIPv4(input); got != want {
			t.Errorf("IsGloballyRoutableIPv4(%q) = %v, want %v", input, got, want)
		}
	}
}
```

- [ ] **Step 2: Run the focused test and verify failure**

Run:

```powershell
rtk go test ./internal/upnp -run TestIsGloballyRoutableIPv4 -count=1 -v
```

Expected: FAIL because `IsGloballyRoutableIPv4` is undefined.

- [ ] **Step 3: Implement public IPv4 classification**

Add:

```go
func IsGloballyRoutableIPv4(raw string) bool {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil || ip.To4() == nil {
		return false
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	_, carrierNAT, _ := net.ParseCIDR("100.64.0.0/10")
	return !carrierNAT.Contains(ip)
}
```

- [ ] **Step 4: Write failing owned-mapping lifecycle tests**

Use an `httptest.Server` as a fake gateway. Record SOAP actions and return:

- success for `AddPortMapping`;
- `8.8.8.8` for `GetExternalIPAddress`;
- success for `DeletePortMapping`.

Assert:

```go
mapping, result := mapTCPWithServices(
	[]gatewayService{fakeService},
	49152,
	49152,
	"ImagePadServer RTSP",
)
if !result.OK || mapping == nil {
	t.Fatalf("map result = %#v, mapping = %#v", result, mapping)
}
if mapping.ExternalIP() != "8.8.8.8" {
	t.Fatalf("ExternalIP = %q", mapping.ExternalIP())
}
if err := mapping.Close(); err != nil {
	t.Fatal(err)
}
if err := mapping.Close(); err != nil {
	t.Fatal(err)
}
if deleteCalls != 1 {
	t.Fatalf("DeletePortMapping calls = %d, want 1", deleteCalls)
}
```

Also assert the generated `AddPortMapping` body contains distinct
`NewExternalPort` and `NewInternalPort` values in a second test.

- [ ] **Step 5: Run lifecycle tests and verify failure**

Run:

```powershell
rtk go test ./internal/upnp -run 'TCPMapping|MapTCP' -count=1 -v
```

Expected: FAIL because `TCPMapping` and `mapTCPWithServices` are undefined.

- [ ] **Step 6: Implement the owned mapping**

Add:

```go
type TCPMapping struct {
	mu           sync.Mutex
	service      gatewayService
	externalPort int
	internalPort int
	externalIP   string
	closed       bool
}

func (m *TCPMapping) ExternalIP() string { return m.externalIP }
func (m *TCPMapping) ExternalPort() int  { return m.externalPort }
func (m *TCPMapping) InternalPort() int  { return m.internalPort }

func (m *TCPMapping) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	svc := m.service
	port := m.externalPort
	m.mu.Unlock()
	return deletePortMapping(svc, port)
}
```

Replace `TryMapTCP` internals with:

```go
func MapTCP(internalPort, externalPort int, description string) (*TCPMapping, Result) {
	services, err := discoverServices()
	if err != nil {
		return nil, Result{Message: err.Error()}
	}
	return mapTCPWithServices(services, internalPort, externalPort, description)
}

func TryMapTCP(port int, description string) Result {
	_, result := MapTCP(port, port, description)
	return result
}
```

Update `addPortMapping` and `tryMapWithService` to accept both port values.
Create `TCPMapping` only after mapping and external-IP lookup succeed.

- [ ] **Step 7: Run tests and commit**

Run:

```powershell
rtk go test ./internal/upnp -count=1
```

Expected: PASS.

Commit:

```powershell
rtk git add internal/upnp/upnp.go internal/upnp/upnp_test.go
rtk git commit -m "feat: own UPnP TCP mappings"
```

---

### Task 2: Expose a standard external-readable RTSP/TCP endpoint

**Files:**
- Modify: `internal/obsrtmp/mediamtx.go`
- Modify: `internal/obsrtmp/mediamtx_test.go`
- Modify: `internal/obsrtmp/manager.go`
- Modify: `internal/obsrtmp/manager_test.go`

- [ ] **Step 1: Write failing MediaMTX URL and authorization tests**

Change the URL expectation to:

```go
if got, want := rt.rtspURL(), "rtsp://192.168.1.50:8554/obs_session"; got != want {
	t.Fatalf("rtspURL = %q, want %q", got, want)
}
```

Replace the private-network-reader assertion with:

```go
func TestRenderMediaMTXConfigAllowsExternalReadersOnRandomPath(t *testing.T) {
	out := renderMediaMTXConfig(defaultTestConfig())
	if !strings.Contains(out, "  - user: any\n    permissions:\n      - action: read\n        path: obs_session\n") {
		t.Fatalf("external read permission missing:\n%s", out)
	}
	if strings.Contains(out, "ips: ['127.0.0.1/32', '10.0.0.0/8'") {
		t.Fatalf("read permission remains LAN-only:\n%s", out)
	}
}
```

Keep the publish user loopback restriction assertion.

- [ ] **Step 2: Run focused tests and verify failure**

Run:

```powershell
rtk go test ./internal/obsrtmp -run 'MediaMTXRuntimeURLs|ExternalReaders' -count=1 -v
```

Expected: FAIL because `rtspURL` does not exist and readers remain IP-limited.

- [ ] **Step 3: Implement standard RTSP URL and path-scoped external reads**

Rename:

```go
func (r *mediaMTXRuntime) rtspURL() string {
	host := strings.TrimSpace(r.cfg.AdvertiseHost)
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("rtsp://%s:%d/%s", host, r.cfg.Ports.RTSP, r.cfg.Path)
}
```

Render authorization as:

```yaml
  - user: any
    permissions:
      - action: read
        path: <random session path>
      - action: playback
        path: <random session path>
      - action: api
      - action: metrics
      - action: pprof
```

Keep API/metrics/pprof loopback-only by splitting them into a separate
loopback user entry. Do not grant public API access.

- [ ] **Step 4: Write failing OBS endpoint callback and URL update tests**

Define the intended contract in tests:

```go
type RTSPEndpoint struct {
	SessionID string
	Host      string
	Port      int
	Path      string
	LocalURL  string
}
```

Assert:

- `OnRTSPReady` receives the active session ID, MediaMTX port and path;
- when OBS connects before publishing is armed, the manager stores the ready
  endpoint and `StartPublishing` re-emits it after arming;
- `SetRTSPURL(sessionID, publicURL, message)` updates the status only for the
  current session;
- passing an old session ID returns `false` and leaves the current status
  untouched;
- stop clears the URL.

- [ ] **Step 5: Run manager tests and verify failure**

Run:

```powershell
rtk go test ./internal/obsrtmp -run 'RTSPEndpoint|SetRTSPURL|RTSPStatus' -count=1 -v
```

Expected: FAIL because the callback and setter are undefined.

- [ ] **Step 6: Implement endpoint callback and guarded URL update**

Extend callbacks:

```go
type Callbacks struct {
	OnStart     func(Session)
	OnDone      func(Session)
	OnRTSPReady func(RTSPEndpoint)
	OnRTSPDone  func(sessionID string)
}
```

Add:

```go
func (m *Manager) SetRTSPURL(sessionID, publicURL, message string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.ID != sessionID ||
		NormalizeLatencyMode(m.status.Latency.Mode) != LatencyModeRTSPT {
		return false
	}
	m.status.RTSPTURL = publicURL
	m.status.Message = message
	return true
}
```

After RTSP readiness, set the local `rtsp://` URL, then invoke
`OnRTSPReady` with the runtime endpoint. Invoke `OnRTSPDone` during every RTSP
session teardown before clearing status. Store the current ready endpoint on
the manager so `StartPublishing` can invoke `OnRTSPReady` after the existing
`OnStart` callback when a stream was connected in preview mode first.

Retain the JSON field name `rtsptURL` for compatibility in this release, while
its value becomes a standard `rtsp://` URL.

- [ ] **Step 7: Update labels and run tests**

Use:

```go
Label:     "リアルタイム（RTSP TCP）",
Transport: LatencyModeRTSPT,
Message:   "PC/Android向けRTSP-over-TCP出力です。",
```

Run:

```powershell
rtk go test ./internal/obsrtmp -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```powershell
rtk git add internal/obsrtmp/mediamtx.go internal/obsrtmp/mediamtx_test.go internal/obsrtmp/manager.go internal/obsrtmp/manager_test.go
rtk git commit -m "feat: expose standard RTSP TCP endpoints"
```

---

### Task 3: Own the public RTSP mapping in the server

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`

- [ ] **Step 1: Add injectable mapping and failing success-path tests**

Add server dependencies:

```go
type rtspMappingHandle interface {
	ExternalIP() string
	ExternalPort() int
	Close() error
}

type rtspPortMapper func(internalPort, externalPort int, description string) (rtspMappingHandle, upnp.Result)
```

Store:

```go
rtspMap       rtspMappingHandle
rtspSessionID string
mapRTSPPort   rtspPortMapper
```

In tests, inject a fake handle and assert an endpoint:

```go
srv.handleRTSPReady(obsrtmp.RTSPEndpoint{
	SessionID: "new-session",
	Port:      49152,
	Path:      "obs_new-session",
	LocalURL:  "rtsp://192.168.1.10:49152/obs_new-session",
})

status := srv.obs.Status()
if got, want := status.RTSPTURL, "rtsp://8.8.8.8:49152/obs_new-session"; got != want {
	t.Fatalf("RTSPTURL = %q, want %q", got, want)
}
```

- [ ] **Step 2: Run the success-path test and verify failure**

Run:

```powershell
rtk go test ./internal/server -run TestRTSPReadyPublishesUPnPURL -count=1 -v
```

Expected: FAIL because the mapping owner and handler do not exist.

- [ ] **Step 3: Implement mapping creation and session ownership**

Wire callbacks in `New`:

```go
OnRTSPReady: srv.handleRTSPReady,
OnRTSPDone:  srv.handleRTSPDone,
```

Initialize:

```go
srv.mapRTSPPort = func(internalPort, externalPort int, description string) (rtspMappingHandle, upnp.Result) {
	mapping, result := upnp.MapTCP(internalPort, externalPort, description)
	return mapping, result
}
```

Implement `handleRTSPReady` to:

1. verify the current mode is `rtspt` and publishing is armed;
2. map the active MediaMTX TCP port;
3. reject non-global external IPv4 addresses;
4. replace and close only the previous owned mapping;
5. call `SetRTSPURL` with
   `rtsp://<external-ip>:<external-port>/<path>`;
6. close the new mapping if the session became stale before installation.

- [ ] **Step 4: Write failing fallback and CGNAT tests**

Cover:

```go
func TestRTSPReadyMappingFailureKeepsLANURL(t *testing.T)
func TestRTSPReadyRejectsCarrierNATAddress(t *testing.T)
func TestRTSPReadyRejectsPrivateExternalAddress(t *testing.T)
```

Assert the OBS session remains active, the URL remains `rtsp://<LAN host>...`,
and status includes the mapping failure or CGNAT explanation.

- [ ] **Step 5: Implement fallback handling**

On failure, call:

```go
srv.obs.SetRTSPURL(endpoint.SessionID, endpoint.LocalURL,
	"RTSP TCP is available on LAN/Tailscale; UPnP publication failed: "+result.Message)
```

For non-public addresses, close the mapping immediately and use:

```text
RTSP TCP is available on LAN/Tailscale; CGNAT or upstream NAT prevents direct publication.
```

- [ ] **Step 6: Write failing cleanup and stale-session tests**

Cover:

- `handleRTSPDone` closes the matching mapping once;
- a stale done callback does not close a newer mapping;
- `StopOBSReceiver` closes the mapping even if OBS teardown does not callback;
- `SyncOBSReceiver` disabling the receiver closes the mapping;
- changing away from `rtspt` closes the mapping;
- installing a newer mapping closes the previous mapping.

- [ ] **Step 7: Implement centralized cleanup**

Add:

```go
func (s *Server) closeRTSPMapping(sessionID string) {
	s.mu.Lock()
	if sessionID != "" && s.rtspSessionID != sessionID {
		s.mu.Unlock()
		return
	}
	mapping := s.rtspMap
	s.rtspMap = nil
	s.rtspSessionID = ""
	s.mu.Unlock()
	if mapping != nil {
		_ = mapping.Close()
	}
}
```

Call it from:

- `handleRTSPDone`;
- `StopOBSReceiver`;
- the receiver-disable path in `SyncOBSReceiver`;
- latency mode changes away from `rtspt`;
- server/application shutdown before returning.

- [ ] **Step 8: Run server tests and commit**

Run:

```powershell
rtk go test ./internal/server -run 'RTSP|OBS' -count=1
```

Expected: PASS.

Commit:

```powershell
rtk git add internal/server/server.go internal/server/server_test.go
rtk git commit -m "feat: publish RTSP TCP through UPnP"
```

---

### Task 4: Use the normal share URL surface

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`
- Modify: `internal/server/ui.go`

- [ ] **Step 1: Write failing share-state tests**

For a ready RTSP session, assert:

```go
state := srv.state(request)
if got, want := state["shareURL"], "rtsp://8.8.8.8:49152/obs_session"; got != want {
	t.Fatalf("shareURL = %q, want %q", got, want)
}
if got, want := state["shareURLLabel"], "RTSP TCP URL"; got != want {
	t.Fatalf("shareURLLabel = %q, want %q", got, want)
}
```

Also verify HLS, LHLS and LL-HLS keep their current HTTP share URL behavior.

- [ ] **Step 2: Run the share-state tests and verify failure**

Run:

```powershell
rtk go test ./internal/server -run TestRTSPUsesNormalShareURL -count=1 -v
```

Expected: FAIL because RTSP is excluded from preview/share URL selection.

- [ ] **Step 3: Implement RTSP share URL selection**

In state construction, when:

```go
obsStatus.Latency.Mode == obsrtmp.LatencyModeRTSPT &&
obsStatus.RTSPTURL != ""
```

set:

```go
shareURL = obsStatus.RTSPTURL
shareURLLabel = "RTSP TCP URL"
```

Do not put the RTSP URL into `PreviewURL`; browsers still cannot preview it.
Allow the server-side copy target to return `shareURL` regardless of HTTP
scheme.

- [ ] **Step 4: Remove the dedicated RTSP UI**

Delete:

```html
<div id="obsRtspt" class="obs-rtspt" style="display:none">...</div>
```

Delete the `applyOBS` block that updates `obsRtsptURL` and the
`obsRtsptCopyBtn` event listener.

Change the option label to:

```html
<option value="rtspt">リアルタイム（RTSP TCP）</option>
```

Update `copyStartedOBSURL` to accept:

```js
if (!url || !(url.startsWith('http') || url.startsWith('rtsp://'))) {
  return;
}
```

- [ ] **Step 5: Add the RTSP public-exposure confirmation dialog**

Add a hidden modal alongside the existing pairing panel:

```html
<div class="modal-backdrop" id="rtspRiskDialog" hidden>
  <section class="modal-card" role="alertdialog" aria-modal="true"
      aria-labelledby="rtspRiskTitle" aria-describedby="rtspRiskDescription">
    <h2 id="rtspRiskTitle">RTSP TCPを外部公開します</h2>
    <div id="rtspRiskDescription">
      <p>グローバルIPアドレスとTCPポートがインターネットへ公開されます。</p>
      <ul>
        <li>RTSP URLを知る第三者が配信へ接続できる可能性があります。</li>
        <li>ルーターのUPnPポート設定を一時的に変更します。</li>
        <li>異常終了やルーター障害時はポート設定が残る可能性があります。</li>
      </ul>
    </div>
    <div class="modal-actions">
      <button type="button" class="secondary" id="rtspRiskCancel">キャンセル</button>
      <button type="button" class="warn" id="rtspRiskConfirm">リスクを理解して有効化</button>
    </div>
  </section>
</div>
```

Add focused `.modal-backdrop`, `.modal-card`, and `.modal-actions` styles using
the existing color variables and responsive breakpoints. The backdrop must use
fixed positioning and the card must fit within the mobile viewport without
horizontal overflow.

Do not store acknowledgement in settings, `localStorage`, cookies, or server
state.

- [ ] **Step 6: Guard the latency change with the dialog**

Track the last server-confirmed mode:

```js
let confirmedOBSLatencyMode = 'hls';
```

When applying server state:

```js
confirmedOBSLatencyMode = latency.mode || 'hls';
obsLatencyMode.value = confirmedOBSLatencyMode;
```

Split the existing API call into:

```js
async function submitOBSLatencyMode(mode) {
  obsLatencyMode.disabled = true;
  obsDVRToggle.disabled = true;
  try {
    const res = await fetch('/api/obs/latency', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        mode,
        dvr: !!(obsDVRToggle && obsDVRToggle.checked),
      }),
    });
    if (!res.ok) throw new Error(await res.text());
    const data = await res.json();
    state.obs = data || null;
    applyOBS(data);
    resetOBSPreview();
    refreshAgain = true;
    announceLocalChange();
  } finally {
    obsLatencyMode.disabled = false;
    obsDVRToggle.disabled = false;
  }
}
```

On selector change:

```js
obsLatencyMode.addEventListener('change', async () => {
  const requestedMode = obsLatencyMode.value;
  if (requestedMode === 'rtspt' && confirmedOBSLatencyMode !== 'rtspt') {
    const accepted = await showRTSPRiskDialog();
    if (!accepted) {
      obsLatencyMode.value = confirmedOBSLatencyMode;
      return;
    }
  }
  await submitOBSLatencyMode(requestedMode);
});
```

`showRTSPRiskDialog` must:

- resolve `true` only from `rtspRiskConfirm`;
- resolve `false` from `rtspRiskCancel`, Escape, or backdrop click;
- restore focus to `obsLatencyMode`;
- remove temporary event listeners after each close;
- open again on every future transition from a non-RTSP mode into `rtspt`.

- [ ] **Step 7: Add UI source and behavior assertions**

Assert `indexHTML`:

- contains `リアルタイム（RTSP TCP）`;
- does not contain `id="obsRtspt"`;
- does not contain `id="obsRtsptCopy"`;
- still contains `id="shareURL"` and `data-copy="shareURL"`;
- contains `role="alertdialog"` and `id="rtspRiskDialog"`;
- contains `リスクを理解して有効化`;
- does not contain RTSP acknowledgement persistence through `localStorage` or
  cookies.

Add a small extracted JavaScript decision helper if needed for deterministic Go
tests. Verify:

- cancelling produces no latency API request and restores the old mode;
- confirming produces one `rtspt` request;
- switching back to HLS and selecting RTSP again requires another confirmation.

- [ ] **Step 8: Run server tests and commit**

Run:

```powershell
rtk go test ./internal/server -count=1
```

Expected: PASS.

Commit:

```powershell
rtk git add internal/server/server.go internal/server/server_test.go internal/server/ui.go
rtk git commit -m "feat: show RTSP in the shared URL field"
```

---

### Task 5: Verify lifecycle, compatibility, and build

**Files:**
- Modify: `docs/OBS_LATENCY_ACCEPTANCE_2026_06_29.md` if the local router test is performed

- [ ] **Step 1: Run formatting**

```powershell
rtk proxy pwsh -NoProfile -Command "gofmt -w internal/upnp/upnp.go internal/upnp/upnp_test.go internal/obsrtmp/mediamtx.go internal/obsrtmp/mediamtx_test.go internal/obsrtmp/manager.go internal/obsrtmp/manager_test.go internal/server/server.go internal/server/server_test.go"
```

Expected: no output.

- [ ] **Step 2: Run focused race-sensitive tests repeatedly**

```powershell
rtk go test ./internal/upnp ./internal/obsrtmp ./internal/server -run 'UPnP|RTSP|MediaMTX|OBS' -count=5
```

Expected: PASS on all five iterations.

- [ ] **Step 3: Run the complete test suite**

```powershell
rtk go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 4: Build the Windows application**

Read the current version from `internal/about/about.go`, then run the
repository's established dev-build command for that version. If the release
script is unavailable in PowerShell, use the existing direct Windows
`go build -trimpath -ldflags "-H=windowsgui"` fallback without changing the
version.

Expected: a new Windows amd64 executable is produced under the current dev
artifact directory.

- [ ] **Step 5: Perform local network smoke testing**

With OBS publishing in real-time mode:

1. Confirm the UI shows `rtsp://<global-ip>:<dynamic-port>/<random-path>` in
   the normal share URL field.
2. Confirm `Get-NetTCPConnection -LocalPort <dynamic-port>` shows MediaMTX
   listening.
3. Confirm MediaMTX logs show RTSP interleaved TCP reads, not UDP RTP ports.
4. Stop the stream and confirm the router mapping disappears.
5. Restart and confirm a new session path cannot be cleared by the old
   session's teardown.

- [ ] **Step 6: Record environment-dependent limitations**

If the router provides CGNAT/private external IP, record that direct public
RTSP could not be smoke-tested, while retaining automated coverage for the
fallback path. Do not claim external VRChat acceptance without an actual
off-LAN playback test.

- [ ] **Step 7: Commit acceptance notes only if changed**

```powershell
rtk git add docs/OBS_LATENCY_ACCEPTANCE_2026_06_29.md
rtk git commit -m "docs: record public RTSP TCP acceptance"
```

---

## Completion Checklist

- [ ] Persisted `rtspt` settings remain compatible.
- [ ] Displayed player URL uses `rtsp://`.
- [ ] MediaMTX permits only RTSP-over-TCP.
- [ ] Public reads are limited to the randomized active path.
- [ ] UPnP mapping is created only for a published RTSP session.
- [ ] Private and CGNAT external addresses are not advertised as public.
- [ ] Stop, restart, mode change, stream completion, and app exit remove the owned mapping.
- [ ] RTSP uses the normal share URL layout and copy path.
- [ ] Selecting RTSP TCP requires an explicit risk confirmation every time.
- [ ] Cancelling the dialog preserves the previous mode without an API call.
- [ ] HLS-family behavior remains unchanged.
- [ ] Full Go tests and Windows build pass.
