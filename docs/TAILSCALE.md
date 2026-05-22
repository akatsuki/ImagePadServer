# Tailscale

ImagePadServer listens on `0.0.0.0` by default, so it can accept requests from a
Tailscale tailnet when the OS firewall allows the configured port.

## Prefer the Tailscale address

Set `IMAGEPAD_PREFER_TAILSCALE=1` before starting the app. When a Tailscale
interface or a `100.64.0.0/10` Tailscale IPv4 address is available, the browser
URL, phone QR code, and local image URLs will use that address.

```powershell
$env:IMAGEPAD_PREFER_TAILSCALE="1"
$env:IMAGEPAD_PORT="8095"
.\imagepadserver.exe
```

## Use a fixed Tailscale host

If you prefer a specific Tailnet IP or MagicDNS name, set
`IMAGEPAD_ADVERTISE_HOST`.

```powershell
$env:IMAGEPAD_ADVERTISE_HOST="100.100.100.100"
$env:IMAGEPAD_PORT="8095"
.\imagepadserver.exe
```

```powershell
$env:IMAGEPAD_ADVERTISE_HOST="my-pc.tailnet-name.ts.net"
$env:IMAGEPAD_PORT="8095"
.\imagepadserver.exe
```

`IMAGEPAD_ADVERTISE_HOST` changes only the URL shown and copied by the app. Use
`IMAGEPAD_HOST` if you need to change the address the HTTP server binds to.
