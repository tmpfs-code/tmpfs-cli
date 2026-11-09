package download

import (
	"strings"
	"unicode"
)

// DefaultName is used when the uploader's filename has nothing usable left.
const DefaultName = "tmpfs-download"

var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// SanitizeName turns an untrusted filename (chosen by the uploader) into a
// safe base name on every OS: no directories, no traversal, no characters or
// names forbidden on Windows.
func SanitizeName(name string) string {
	// keep only the last path element, for both separators
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	// drive letters / alternate data streams
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[i+1:]
	}

	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f || unicode.IsControl(r):
			continue
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	// Windows strips trailing dots and spaces; leading/trailing spaces are confusing
	name = strings.TrimRight(strings.TrimSpace(b.String()), ". ")

	if name == "" || name == "." || name == ".." {
		return DefaultName
	}

	stem := name
	if i := strings.Index(stem, "."); i >= 0 {
		stem = stem[:i]
	}
	if windowsReserved[strings.ToUpper(strings.TrimSpace(stem))] {
		name = "_" + name
	}

	// keep names within common filesystem limits (255 bytes)
	for len(name) > 200 {
		r := []rune(name)
		name = string(r[:len(r)-1])
	}
	return name
}
