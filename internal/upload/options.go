package upload

import (
	"fmt"
	"strings"
)

// Durations accepted by the server, in the order shown to the user.
var Durations = []struct {
	Name    string
	Seconds int
	Free    bool
}{
	{"10m", 600, true},
	{"1h", 3600, true},
	{"6h", 21600, true},
	{"12h", 43200, true},
	{"1d", 86400, true},
	{"3d", 259200, false},
	{"1w", 604800, false},
}

// DefaultDuration is used when no duration is given.
const DefaultDuration = "1d"

// MaxDownloads is fixed for now (the webapp default). Valid values are
// 1, 10, 50 (free) and 100, 1000 (paid).
const MaxDownloads = 10

var paidMaxDownloads = map[int]bool{100: true, 1000: true}

const (
	// MaxFileSize is the largest file that can be uploaded (MAX_FILE_SIZE in
	// the webapp, maxEncFileSize on the server).
	MaxFileSize int64 = 2_000_000_000
	// MaxFreeSizeRange is the largest free upload, in MB (1 MB = 1e6 bytes).
	MaxFreeSizeRange = 200
)

// DurationNames returns the valid duration names, space separated.
func DurationNames() string {
	names := make([]string, len(Durations))
	for i, d := range Durations {
		names[i] = d.Name
	}
	return strings.Join(names, " ")
}

// ParseDuration converts a duration name (e.g. "1d") into seconds.
func ParseDuration(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, d := range Durations {
		if d.Name == s {
			return d.Seconds, nil
		}
	}
	return 0, fmt.Errorf("invalid duration %q, valid values: %s", s, DurationNames())
}

// SizeRange returns the size range used for pricing: whole MB, rounded down.
func SizeRange(size int64) int {
	return int(size / 1_000_000)
}

// IsPaid reports whether an upload is in the paid tier (STEERING.md §4.4).
func IsPaid(size int64, lifetimeSec, maxDownloads int) bool {
	if SizeRange(size) > MaxFreeSizeRange {
		return true
	}
	if paidMaxDownloads[maxDownloads] {
		return true
	}
	for _, d := range Durations {
		if d.Seconds == lifetimeSec {
			return !d.Free
		}
	}
	return true
}

// PaidReason explains why an upload is in the paid tier.
func PaidReason(size int64, lifetimeSec int) string {
	var reasons []string
	if SizeRange(size) > MaxFreeSizeRange {
		reasons = append(reasons, fmt.Sprintf("files larger than %d MB", MaxFreeSizeRange))
	}
	for _, d := range Durations {
		if d.Seconds == lifetimeSec && !d.Free {
			reasons = append(reasons, "durations longer than 1d")
		}
	}
	return strings.Join(reasons, " and ")
}
