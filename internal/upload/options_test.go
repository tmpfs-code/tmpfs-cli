package upload

import "testing"

func TestParseDuration(t *testing.T) {
	valid := map[string]int{"10m": 600, "1h": 3600, "6h": 21600, "12h": 43200, "1d": 86400, "3d": 259200, "1w": 604800, " 1D ": 86400}
	for in, want := range valid {
		if got, err := ParseDuration(in); err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "1", "24h", "2d", "1m", "7d", "-1d"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) must fail", in)
		}
	}
}

func TestIsPaid(t *testing.T) {
	const mb = 1_000_000
	cases := []struct {
		size     int64
		lifetime int
		maxDl    int
		paid     bool
	}{
		{1, 86400, 10, false},
		{200*mb + 999_999, 86400, 10, false}, // still range 200
		{201 * mb, 86400, 10, true},
		{1, 259200, 10, true},
		{1, 604800, 10, true},
		{1, 600, 50, false},
		{1, 600, 100, true},
		{1, 1234, 10, true}, // unknown lifetime: never free
	}
	for _, c := range cases {
		if got := IsPaid(c.size, c.lifetime, c.maxDl); got != c.paid {
			t.Errorf("IsPaid(%d, %d, %d) = %v, want %v", c.size, c.lifetime, c.maxDl, got, c.paid)
		}
	}
}
