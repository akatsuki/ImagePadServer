package server

import (
	"strconv"
	"strings"
)

func versionGreater(candidate, current string) bool {
	candidateParts := versionParts(candidate)
	currentParts := versionParts(current)
	for i := 0; i < len(candidateParts) && i < len(currentParts); i++ {
		if candidateParts[i] > currentParts[i] {
			return true
		}
		if candidateParts[i] < currentParts[i] {
			return false
		}
	}
	return false
}

func versionParts(version string) [3]int {
	version = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(version)), "v")
	version = strings.Split(version, "-")[0]
	parts := strings.Split(version, ".")
	var result [3]int
	for i := 0; i < len(parts) && i < len(result); i++ {
		n, _ := strconv.Atoi(parts[i])
		result[i] = n
	}
	return result
}
