# ytdlplab

Small, isolated yt-dlp probe environment for YouTube extractor checks.

It does not download media by default. It records JSON reports for:

- `--list-formats`
- ImagePadServer's production format selector with `--simulate`
- ImagePadServer's production YouTube impersonation attempts with `--simulate`
- optional Downie 4 resource scan
- optional external Downie runner invocation on macOS

Example on Windows:

```powershell
go run ./cmd/ytdlplab `
  --url "https://www.youtube.com/watch?v=tc6SxHVGrbE" `
  --yt-dlp "$env:APPDATA\ImagePadServer\bin\v1.5.0-dev8\yt-dlp.exe" `
  --cookies `
  --downie-app "E:\Downie 4.app" `
  --out ".dev\ytdlplab"
```

Example Downie black-box submission on macOS:

```bash
go run ./cmd/ytdlplab \
  --url "https://www.youtube.com/watch?v=tc6SxHVGrbE" \
  --yt-dlp "/path/to/yt-dlp" \
  --cookies \
  --downie-app "/Applications/Downie 4.app" \
  --downie-runner "./tools/ytdlplab/downie-open.sh"
```

`downie-open.sh` submits the URL to Downie and reports only that it was handed
off. Confirming completion still depends on Downie's UI, destination folder, or
history/log observation on the Mac.
