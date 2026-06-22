package about

const (
	AppName        = "ImagePadServer"
	Version        = "v1.4.5-dev1"
	FileVersion    = "1.4.5.1"
	Author         = "Akat / 赤月さん"
	License        = "MIT License"
	Copyright      = "Copyright (c) 2026 Akat / 赤月さん"
	Description    = "VRChat ImagePad upload helper"
	HomePage       = "https://github.com/"
	CloudflaredURL = "https://github.com/cloudflare/cloudflared"
)

type OpenSourceNotice struct {
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
	License   string `json:"license"`
	Copyright string `json:"copyright"`
	URL       string `json:"url,omitempty"`
	Note      string `json:"note,omitempty"`
}

var OpenSourceNotices = []OpenSourceNotice{
	{
		Name:      "github.com/skip2/go-qrcode",
		Version:   "v0.0.0-20200617195104-da1b6568686e",
		License:   "MIT License",
		Copyright: "Copyright (c) 2014 Tom Harwood",
		URL:       "https://github.com/skip2/go-qrcode",
	},
	{
		Name:      "github.com/srwiley/oksvg",
		Version:   "v0.0.0-20221011165216-be6e8873101c",
		License:   "BSD 3-Clause License",
		Copyright: "Copyright (c) 2018 Steven R Wiley",
		URL:       "https://github.com/srwiley/oksvg",
	},
	{
		Name:      "github.com/srwiley/rasterx",
		Version:   "v0.0.0-20220730225603-2ab79fcdd4ef",
		License:   "BSD 3-Clause License",
		Copyright: "Copyright (c) 2018 Steven R Wiley",
		URL:       "https://github.com/srwiley/rasterx",
	},
	{
		Name:      "golang.org/x/image",
		Version:   "v0.40.0",
		License:   "BSD 3-Clause License",
		Copyright: "Copyright 2009 The Go Authors",
		URL:       "https://pkg.go.dev/golang.org/x/image",
	},
	{
		Name:      "golang.org/x/net",
		Version:   "v0.53.0",
		License:   "BSD 3-Clause License",
		Copyright: "Copyright 2009 The Go Authors",
		URL:       "https://pkg.go.dev/golang.org/x/net",
	},
	{
		Name:      "golang.org/x/text",
		Version:   "v0.37.0",
		License:   "BSD 3-Clause License",
		Copyright: "Copyright 2009 The Go Authors",
		URL:       "https://pkg.go.dev/golang.org/x/text",
	},
	{
		Name:      "cloudflared",
		License:   "Apache License 2.0",
		Copyright: "Copyright (c) Cloudflare, Inc.",
		URL:       CloudflaredURL,
		Note:      "Bundled separately by user install/download at first tunnel use; not embedded in the executable.",
	},
}
