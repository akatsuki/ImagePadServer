package video

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
)

// VisualizerFontFace binds a font file path to its resolved ASS-compatible
// family name and PostScript name.  The ASSFamily value is the font's Name ID 1
// (Font Family) parsed from the OTF name table, NOT a filesystem path.  The
// PostScriptName is the font's Name ID 6 and is used for precise weight
// selection when measuring text widths via the ASS render pipeline (AV-824).
type VisualizerFontFace struct {
	FilePath       string
	ASSFamily      string
	PostScriptName string
}

// VisualizerFontFaces holds one VisualizerFontFace for each weight.
type VisualizerFontFaces struct {
	Regular400  VisualizerFontFace
	Medium500   VisualizerFontFace
	SemiBold600 VisualizerFontFace
}

// ResolveVisualizerFontFaces reads each OTF font file from the given FontSet,
// parses its name table, and returns one VisualizerFontFaces with the
// resolved family name for each weight.
//
// Each ASSFamily is the Windows-platform (platform=3) Font Family name
// (Name ID 1) from the font's OTF name table.  Empty names and names that
// look like a filesystem path (contain '/' or '\') are rejected.
//
// The destination field (Regular400/Medium500/SemiBold600) is identified by
// the font's PostScript name (Name ID 6), so the caller does not need to
// rely on positional ordering.
func ResolveVisualizerFontFaces(fonts FontSet) (VisualizerFontFaces, error) {
	entries := []struct {
		label string
		path  string
	}{
		{"Regular400", fonts.Regular400},
		{"Medium500", fonts.Medium500},
		{"SemiBold600", fonts.SemiBold600},
	}
	var result VisualizerFontFaces
	for _, e := range entries {
		family, err := readOTFFamilyName(e.path)
		if err != nil {
			return result, fmt.Errorf("resolve %s %s: %w", e.label, e.path, err)
		}
		psName, err := readOTFPostScriptName(e.path)
		if err != nil {
			return result, fmt.Errorf("resolve %s %s (postscript): %w", e.label, e.path, err)
		}
		weight := parsePostScriptWeight(psName)
		face := VisualizerFontFace{FilePath: e.path, ASSFamily: family, PostScriptName: psName}
		switch weight {
		case "Regular":
			result.Regular400 = face
		case "Medium":
			result.Medium500 = face
		case "SemiBold":
			result.SemiBold600 = face
		default:
			// Unknown or missing weight — fall back to label order.
			switch e.label {
			case "Regular400":
				result.Regular400 = face
			case "Medium500":
				result.Medium500 = face
			case "SemiBold600":
				result.SemiBold600 = face
			}
		}
	}
	return result, nil
}

// readOTFFamilyName reads the Font Family name (Name ID 1) from an OTF file's
// 'name' table.  It prefers the Windows platform (platform=3) record; the
// first Name ID 1 match on that platform is returned.
func readOTFFamilyName(path string) (string, error) {
	return readOTFNameID(path, 1)
}

// readOTFPostScriptName reads the PostScript name (Name ID 6) from an OTF
// file's 'name' table.
func readOTFPostScriptName(path string) (string, error) {
	return readOTFNameID(path, 6)
}

// readOTFNameID reads a specific Name ID from an OTF file's 'name' table.
// It prefers the Windows platform (platform=3) record; the first match for
// the given nameID on that platform is returned.
func readOTFNameID(path string, targetID uint16) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) < 12 {
		return "", errors.New("file too small for OTF header")
	}

	numTables := int(binary.BigEndian.Uint16(data[4:6]))
	if numTables == 0 {
		return "", errors.New("no tables in font directory")
	}

	// Search the table directory for the 'name' table.
	var nameOffset, nameLength uint32
	found := false
	for i := 0; i < numTables; i++ {
		entryOff := 12 + i*16
		if entryOff+16 > len(data) {
			break
		}
		tag := string(data[entryOff : entryOff+4])
		if tag == "name" {
			nameOffset = binary.BigEndian.Uint32(data[entryOff+8 : entryOff+12])
			nameLength = binary.BigEndian.Uint32(data[entryOff+12 : entryOff+16])
			found = true
			break
		}
	}
	if !found {
		return "", errors.New("name table not found")
	}

	// Validate the 'name' table bounds.
	if int(nameOffset)+6 > len(data) {
		return "", errors.New("name table offset out of range")
	}
	if nameLength < 6 {
		return "", errors.New("name table too small")
	}

	// Read the name table header.
	// Format 0 or 1: format(2), count(2), stringOffset(2)
	format := binary.BigEndian.Uint16(data[nameOffset : nameOffset+2])
	if format > 1 {
		return "", fmt.Errorf("unsupported name table format %d", format)
	}
	count := int(binary.BigEndian.Uint16(data[nameOffset+2 : nameOffset+4]))
	if count == 0 {
		return "", errors.New("name table has zero records")
	}
	stringOffset := int(binary.BigEndian.Uint16(data[nameOffset+4 : nameOffset+6]))

	// Iterate name records looking for the target Name ID.
	// Prefer Windows platform (platform=3).
	var winMatch, otherMatch string
	for j := 0; j < count; j++ {
		recOff := nameOffset + 6 + uint32(j*12)
		if int(recOff)+12 > len(data) {
			break
		}
		platformID := binary.BigEndian.Uint16(data[recOff : recOff+2])
		// encodingID at recOff+2 (unused)
		nameID := binary.BigEndian.Uint16(data[recOff+6 : recOff+8])
		length := int(binary.BigEndian.Uint16(data[recOff+8 : recOff+10]))
		offset := int(binary.BigEndian.Uint16(data[recOff+10 : recOff+12]))

		if nameID != targetID {
			continue
		}

		strStart := int(nameOffset) + stringOffset + offset
		if strStart+length > len(data) {
			continue
		}

		raw := data[strStart : strStart+length]
		if len(raw) == 0 {
			continue
		}

		var name string
		if platformID == 3 {
			// Windows platform: UTF-16BE encoding
			if length < 2 {
				continue
			}
			name = decodeUTF16BE(raw)
		} else if platformID == 1 {
			// Macintosh platform: ASCII/MacRoman
			name = string(raw)
		} else {
			continue
		}

		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		// Reject path-like names
		if strings.ContainsAny(name, `/\`) {
			continue
		}

		if platformID == 3 {
			if winMatch == "" {
				winMatch = name
			}
		} else if otherMatch == "" {
			otherMatch = name
		}
	}

	if winMatch != "" {
		return winMatch, nil
	}
	if otherMatch != "" {
		return otherMatch, nil
	}
	return "", fmt.Errorf("name ID %d not found in name table", targetID)
}

// parsePostScriptWeight extracts the weight name from a PostScript font name.
// For example "NotoSansCJKjp-Regular" returns "Regular",
// "NotoSansCJKjp-Medium" returns "Medium",
// "NotoSansCJKjp-SemiBold" returns "SemiBold".
func parsePostScriptWeight(psName string) string {
	i := strings.LastIndex(psName, "-")
	if i < 0 {
		return ""
	}
	return psName[i+1:]
}

// decodeUTF16BE converts big-endian UTF-16 bytes to a Go string,
// handling surrogate pairs.
func decodeUTF16BE(raw []byte) string {
	if len(raw) < 2 {
		return ""
	}
	runes := make([]rune, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		hi := binary.BigEndian.Uint16(raw[i:])
		if hi >= 0xD800 && hi <= 0xDBFF && i+3 < len(raw) {
			// High surrogate - expect low surrogate
			lo := binary.BigEndian.Uint16(raw[i+2:])
			if lo >= 0xDC00 && lo <= 0xDFFF {
				runes = append(runes, rune(0x10000+(uint32(hi)-0xD800)*0x400+(uint32(lo)-0xDC00)))
				i += 2
				continue
			}
		}
		if hi >= 0xD800 && hi <= 0xDFFF {
			// Unpaired surrogate - skip
			continue
		}
		runes = append(runes, rune(hi))
	}
	return string(runes)
}
