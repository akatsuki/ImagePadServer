# X post image conversion

Pasting a public `x.com` or `twitter.com` status URL into ImagePadServer's URL upload flow creates a still image of the post. The generated layout follows the prototype: Noto Sans JP text, the current light or dark theme, up to four attached images, video posters, quoted or reposted content, and link previews. The canvas uses a 2048-pixel long edge and a 4:3 or 3:4 aspect ratio; image conversion options still control the final file format and quality.

The release archive includes the pinned `react-tweet` package files beside the server executable. Node.js 20 or later must be installed and available as `node` on `PATH`. Set `IMAGEPAD_NODE` to an executable path if Node.js is installed outside `PATH`. Public posts are supported; deleted or private posts cannot be fetched.

For local development, install the pinned fetch dependency with:

```powershell
npm ci --prefix internal/xpostimage
```

The release build runs this install step and places the fetch script and package files in the Windows ZIP, macOS app bundle, and Linux output directory. Direct image and card downloads continue to use the server's public-address validation and download limits.
