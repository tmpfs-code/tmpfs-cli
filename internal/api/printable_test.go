package api

import "testing"

func TestPrintable(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain message", "plain message"},
		{"\x1b]0;pwned\x07title\x1b[2J", "]0;pwned title [2J"},
		{"line1\nline2\r", "line1 line2"},
		{"\u009b31m", "31m"}, // C1 CSI
		{"abcdef", "abc..."},
	} {
		max := 200
		if tc.in == "abcdef" {
			max = 3
		}
		if got := printable(tc.in, max); got != tc.want {
			t.Errorf("printable(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
