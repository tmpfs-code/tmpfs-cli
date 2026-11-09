package download

import (
	"strings"
	"testing"
)

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"report.pdf":              "report.pdf",
		"../../etc/passwd":        "passwd",
		`..\..\Windows\win.ini`:   "win.ini",
		"C:evil.exe":              "evil.exe",
		"file:stream":             "stream",
		"a<b>c|d?e*f\".txt":       "a_b_c_d_e_f_.txt",
		"tab\there\x00.txt":       "tabhere.txt",
		"  spaced  ":              "spaced",
		"trailing...":             "trailing",
		"CON":                     "_CON",
		"nul.txt":                 "_nul.txt",
		"com1.tar.gz":             "_com1.tar.gz",
		"console.log":             "console.log",
		"":                        DefaultName,
		".":                       DefaultName,
		"..":                      DefaultName,
		"/":                       DefaultName,
		"日本語 ファイル.zip":            "日本語 ファイル.zip",
		"résumé <final> & co.pdf": "résumé _final_ & co.pdf",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}

	long := SanitizeName(strings.Repeat("é", 300) + ".bin")
	if len(long) > 200 {
		t.Errorf("long name not shortened: %d bytes", len(long))
	}
}
