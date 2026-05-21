# ImagePadServer Notice

ImagePadServer v1.0.0-rc.1

Copyright (c) 2026 Akat / 赤月さん

ImagePadServer is licensed under the MIT License. See `LICENSE`.

## Open Source Software

The application uses the following open source components:

| Component | Version | License | Copyright |
| --- | --- | --- | --- |
| github.com/skip2/go-qrcode | v0.0.0-20200617195104-da1b6568686e | MIT License | Copyright (c) 2014 Tom Harwood |
| github.com/srwiley/oksvg | v0.0.0-20221011165216-be6e8873101c | BSD 3-Clause License | Copyright (c) 2018 Steven R Wiley |
| github.com/srwiley/rasterx | v0.0.0-20220730225603-2ab79fcdd4ef | BSD 3-Clause License | Copyright (c) 2018 Steven R Wiley |
| golang.org/x/image | v0.40.0 | BSD 3-Clause License | Copyright 2009 The Go Authors |
| golang.org/x/net | v0.53.0 | BSD 3-Clause License | Copyright 2009 The Go Authors |
| golang.org/x/text | v0.37.0 | BSD 3-Clause License | Copyright 2009 The Go Authors |
| cloudflared | downloaded separately at first tunnel use | Apache License 2.0 | Copyright (c) Cloudflare, Inc. |

`cloudflared.exe` is not embedded in the ImagePadServer executable. When Cloudflare Tunnel is used on Windows, ImagePadServer downloads it to the user's application data directory.
