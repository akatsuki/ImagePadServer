# X post image conversion

Pasting a public `x.com` or `twitter.com` status URL into ImagePadServer's URL upload flow creates a still image of the post. The generated layout follows the prototype: Noto Sans JP text, the current light or dark theme, up to four attached images, video posters, quoted or reposted content, and link previews. The canvas uses a 2048-pixel long edge and a 4:3 or 3:4 aspect ratio; image conversion options still control the final file format and quality.

The pinned `react-tweet` fetcher bundle is embedded in the server executable, so the release archive does not carry a separate `x-post-image` directory or `node_modules` tree. Node.js 20 or later must still be installed and available as `node` on `PATH`. Set `IMAGEPAD_NODE` to an executable path if Node.js is installed outside `PATH`. Public posts are supported; deleted or private posts cannot be fetched.

For local development, install the pinned fetch dependencies and regenerate the embedded bundle with:

```powershell
npm ci --prefix internal/xpostimage
npm run build:fetcher --prefix internal/xpostimage
```

The release build runs both commands before compiling the Go executables. The bundle retains its third-party license comments. Direct image and card downloads continue to use the server's public-address validation and download limits.
