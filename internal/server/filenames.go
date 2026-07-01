package server

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

func safeFileName(name string) string {
	name = strings.ReplaceAll(name, `"`, "")
	name = strings.ReplaceAll(name, `\`, "")
	if name == "" {
		return "current"
	}
	return name
}

func randomSuffix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}
