package iphonemodel

import "encoding/base64"

const (
	fairPlaySetup1Base64 = "RlBMWQMBAQAAAAAEAgADuw=="
	fairPlaySetup2Base64 = "RlBMWQMBAwAAAACYAI8anOyH5YMFYJ2+xr3Togh8xUSE+qbKGb01dyo1zQEdBiCJO8IsijZ9rWu2klH4mQgUPj5UPvPARqE3nloQ/4sF5MtQBLluSPLdZAJBgQcv6p84y85wYR/LP9bxsGXW2LUWaDl4GvTw4nu8cccOMKV2Jm4aA9j51YBS2CCR3zs6Nin1sue/0us9jl1nzrkhFYv2dXySXE4="
	setupBase64          = "YnBsaXN0MDDVAQIDBAUGBwgJClhkZXZpY2VJRFNlaXZUZWtleVVtb2RlbFRuYW1lXxARMDA6MTE6MjI6MzM6NDQ6NTVPEBAAAQIDBAUGBwgJCgsMDQ4PTxBIAAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0+P0BBQkNERUZHVmlQaG9uZV1JbWFnZVBhZCBUZXN0CBMcICUrMERXoqkAAAAAAAABAQAAAAAAAAALAAAAAAAAAAAAAAAAAAAAtw=="
)

func mustDecodeFixture(encoded string) []byte {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		panic("invalid embedded AirPlay fixture: " + err.Error())
	}
	return decoded
}
