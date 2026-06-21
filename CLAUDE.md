# ImagePadServer — Project Rules

## Dev builds are ALWAYS paired with a commit (non-negotiable)

Never produce or "cut" a `v1.3.0-devN` build without committing the matching
source state in the **same step**. A built dev binary must always correspond to
a committed tree.

**Why:** uncommitted work on this repo has been silently lost before (an
external agent's revert wiped definitions while their references stayed). Tying
every dev build to a `chore: cut v1.3.0-devN` commit guarantees the binary is
reproducible and the work is recoverable from git, not just the working tree.

### Cutting a dev build — do all of this together

1. Gate: `go build ./...` and `go test ./...` are green first.
2. Increment the monotonic version in BOTH:
   - `internal/about/about.go` — `Version = "v1.3.0-devN"`, `FileVersion = "1.3.0.N"`
   - `winres/winres.json` — `version`, `file_version`, `product_version`,
     `FileVersion`, `ProductVersion`
3. Regenerate Windows resources: `cmd/imagepadserver/rsrc_windows_amd64.syso`
   (go-winres), then build the versioned Windows artifact.
4. **Commit the whole set in one commit**: `chore: cut v1.3.0-devN` with a
   one-line summary of what changed. End with:
   `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`

Do not stop after building. Build without commit = rule violation.

## General

- Build artifacts, `outputs/`, and `youtube_cookies.txt` are gitignored and must
  never be committed (`youtube_cookies.txt` is a secret).
