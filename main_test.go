package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/tmpfs-code/tmpfs-cli/internal/clearance"
	"github.com/tmpfs-code/tmpfs-cli/internal/tmpcrypto"
	"github.com/tmpfs-code/tmpfs-cli/internal/upload"
)

// fakeServer mimics the tmpfs server API and its rules (sequential chunks,
// clearance on the first chunk, header on the last, nginx 503s).
type fakeServer struct {
	t *testing.T

	mu         sync.Mutex
	files      map[string][]byte
	headers    map[string]map[string]any
	promos     map[string]bool // code -> unused
	promoIDs   map[string]bool // ids handed out by promo redemption
	secret     string
	reject503  int // reject every Nth request with 503 (0: never)
	reqCount   int
	truncateDL bool
	userAgents map[string]bool
}

func newFakeServer(t *testing.T) (*fakeServer, *httptest.Server) {
	fs := &fakeServer{
		t: t, files: map[string][]byte{}, headers: map[string]map[string]any{},
		promos: map[string]bool{"GOOD-CODE": true}, promoIDs: map[string]bool{},
		secret: "0a0b0c0d0e0f1011", userAgents: map[string]bool{},
	}
	srv := httptest.NewServer(fs)
	t.Cleanup(srv.Close)
	return fs, srv
}

func (s *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reqCount++
	s.userAgents[r.Header.Get("User-Agent")] = true
	if s.reject503 > 0 && s.reqCount%s.reject503 == 0 {
		http.Error(w, "503 Service Temporarily Unavailable", http.StatusServiceUnavailable)
		return
	}
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")

	switch {
	case r.URL.Path == "/api/v1/upload/clearance" && r.Method == http.MethodPost:
		if r.Header.Get("User-Agent") == "" || r.Header.Get("Accept-Language") == "" || r.Header.Get("Accept-Encoding") == "" {
			reply(200, map[string]int{"err": 1})
			return
		}
		fmt.Fprintf(w, `{"s": "%s", "d": 3}`, s.secret)

	case r.URL.Path == "/api/v1/promo/redeem" && r.Method == http.MethodPost:
		var req struct {
			Code string `json:"co"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if !s.promos[req.Code] {
			reply(400, map[string]string{"error": "invalid promo code"})
			return
		}
		s.promos[req.Code] = false
		id := "11111111-2222-4333-8444-555555555555"
		s.promoIDs[id] = true
		reply(200, map[string]string{"id": id})

	case len(parts) == 6 && parts[2] == "upload" && r.Method == http.MethodPost:
		id := parts[3]
		from, _ := strconv.ParseInt(parts[4], 10, 64)
		to, _ := strconv.ParseInt(parts[5], 10, 64)
		body, _ := io.ReadAll(r.Body)
		abort := func(msg string) {
			delete(s.files, id)
			s.t.Logf("fake server: abort upload: %s", msg)
			reply(500, map[string]string{"error": "Internal Server Error"})
		}
		if int64(len(body)) != to-from || to-from > 2_100_000 {
			abort("body size")
			return
		}
		if from == 0 && !clearance.Check(s.secret, r.Header.Get("x-clearance"), 4) {
			abort("clearance")
			return
		}
		cur := s.files[id]
		if from > int64(len(cur)) {
			abort("from beyond size")
			return
		}
		s.files[id] = append(cur[:from], body...)
		if h := r.URL.Query().Get("h"); h != "" {
			data, err := base64.RawURLEncoding.DecodeString(h)
			var hdr map[string]any
			if err != nil || json.Unmarshal(data, &hdr) != nil {
				abort("header")
				return
			}
			hdr["c"] = 0
			s.headers[id] = hdr
		}
		reply(200, map[string]string{})

	case len(parts) == 5 && parts[2] == "download" && parts[4] == "header":
		hdr, ok := s.headers[parts[3]]
		if !ok {
			reply(404, map[string]string{"error": "Not Found"})
			return
		}
		reply(200, map[string]any{"fm": hdr, "dl": 9, "tl": 86000})

	case len(parts) == 4 && parts[2] == "download":
		data, ok := s.files[parts[3]]
		if !ok || s.headers[parts[3]] == nil {
			reply(500, map[string]string{"error": "Internal Server Error"})
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		if s.truncateDL {
			w.Write(data[:len(data)/2])
			return // the client sees an unexpected EOF
		}
		w.Write(data)

	default:
		reply(404, map[string]string{"error": "Not Found"})
	}
}

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr)
	return code, strings.TrimSpace(stdout.String()), stderr.String()
}

func writeRandomFile(t *testing.T, dir, name string, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	rand.Read(data)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRoundTrip(t *testing.T) {
	fs, srv := newFakeServer(t)
	fs.reject503 = 3 // every 3rd request is rejected by "nginx"

	dir := t.TempDir()
	sizes := []int{1, 15, 16, 17, upload.ChunkSize - 1, upload.ChunkSize, upload.ChunkSize + 1, 2*upload.ChunkSize + 12345}
	for _, size := range sizes {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			name := fmt.Sprintf("in-%d.bin", size)
			want := writeRandomFile(t, dir, name, size)

			code, link, stderr := runCLI(t, "--server", srv.URL, "u", filepath.Join(dir, name), "-t", "1h")
			if code != 0 {
				t.Fatalf("upload: exit %d: %s", code, stderr)
			}
			if !strings.HasPrefix(link, srv.URL+"/file/") {
				t.Fatalf("unexpected link %q", link)
			}

			out := filepath.Join(dir, "out")
			os.MkdirAll(out, 0o755)
			code, _, stderr = runCLI(t, "-q", "download", link, "-o", out)
			if code != 0 {
				t.Fatalf("download: exit %d: %s", code, stderr)
			}
			got, err := os.ReadFile(filepath.Join(out, name))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("downloaded file differs (err=%v, %d vs %d bytes)", err, len(got), len(want))
			}
		})
	}

	for ua := range fs.userAgents {
		if !strings.HasPrefix(ua, "tmpfs-cli/") {
			t.Errorf("unexpected User-Agent %q", ua)
		}
	}
}

func TestPaidTier(t *testing.T) {
	_, srv := newFakeServer(t)
	dir := t.TempDir()
	writeRandomFile(t, dir, "small.bin", 100)
	path := filepath.Join(dir, "small.bin")

	// 3d needs a promo code: refused before any request
	code, _, stderr := runCLI(t, "--server", srv.URL, "u", "-t", "3d", path)
	if code != 1 || !strings.Contains(stderr, "promo code") {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	code, _, stderr = runCLI(t, "--server", srv.URL, "u", "-t", "3d", "-p", "BAD", path)
	if code != 1 || !strings.Contains(stderr, "invalid promo code") {
		t.Fatalf("bad code: exit %d: %s", code, stderr)
	}

	code, link, stderr := runCLI(t, "--server", srv.URL, "u", "-t", "3d", "-p", "GOOD-CODE", path)
	if code != 0 || !strings.Contains(link, "/file/11111111-2222-4333-8444-555555555555/") {
		t.Fatalf("good code: exit %d, link %q: %s", code, link, stderr)
	}

	// a promo code on a free upload is not redeemed
	code, _, stderr = runCLI(t, "--server", srv.URL, "u", "-p", "GOOD-CODE", path)
	if code != 0 || !strings.Contains(stderr, "promo code was not used") {
		t.Fatalf("free upload with code: exit %d: %s", code, stderr)
	}
}

func TestDownloadSafety(t *testing.T) {
	fs, srv := newFakeServer(t)
	dir := t.TempDir()
	writeRandomFile(t, dir, "f.bin", 5000)
	code, link, stderr := runCLI(t, "--server", srv.URL, "u", filepath.Join(dir, "f.bin"))
	if code != 0 {
		t.Fatal(stderr)
	}
	out := filepath.Join(dir, "out.bin")

	// truncated transfer: error, and neither the file nor the .part remains
	fs.truncateDL = true
	code, _, stderr = runCLI(t, "d", link, "-o", out)
	if code != 1 || !strings.Contains(stderr, "download failed") {
		t.Fatalf("truncated: exit %d: %s", code, stderr)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("a truncated download left the target file")
	}
	if _, err := os.Stat(out + ".part"); err == nil {
		t.Fatal("a truncated download left the .part file")
	}
	fs.truncateDL = false

	// existing file: refused without -f, replaced with -f
	os.WriteFile(out, []byte("old"), 0o644)
	if code, _, stderr = runCLI(t, "d", link, "-o", out); code != 1 || !strings.Contains(stderr, "-f") {
		t.Fatalf("existing: exit %d: %s", code, stderr)
	}
	if code, _, stderr = runCLI(t, "d", "-f", link, "-o", out); code != 0 {
		t.Fatalf("force: exit %d: %s", code, stderr)
	}
	if fi, _ := os.Stat(out); fi.Size() != 5000 {
		t.Fatalf("forced download has %d bytes", fi.Size())
	}

	// a leftover .part is never written through: refused without -f, and
	// with -f it is replaced (a symlink there must not be followed)
	victim := filepath.Join(dir, "victim")
	os.WriteFile(victim, []byte("keep"), 0o644)
	os.Remove(out)
	if err := os.Symlink(victim, out+".part"); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr = runCLI(t, "d", link, "-o", out); code != 1 || !strings.Contains(stderr, ".part") {
		t.Fatalf("existing .part: exit %d: %s", code, stderr)
	}
	if code, _, stderr = runCLI(t, "d", "-f", link, "-o", out); code != 0 {
		t.Fatalf("force over .part: exit %d: %s", code, stderr)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatal("the download wrote through a symlink at the .part path")
	}

	// wrong key
	bad := link[:len(link)-3] + "abc"
	if code, _, stderr = runCLI(t, "d", bad, "-o", filepath.Join(dir, "x")); code == 0 {
		t.Fatalf("wrong key accepted: %s", stderr)
	}

	// expired / unknown id
	l, _ := tmpcrypto.ParseLink(link)
	unknown := tmpcrypto.FormatLink(srv.URL, "99999999-9999-4999-8999-999999999999", l.Key)
	if code, _, stderr = runCLI(t, "d", unknown, "-o", filepath.Join(dir, "y")); code != 1 || !strings.Contains(stderr, "expired") {
		t.Fatalf("unknown id: exit %d: %s", code, stderr)
	}
}

func TestHostileFilename(t *testing.T) {
	_, srv := newFakeServer(t)
	dir := t.TempDir()
	name := "evil\x1b]0;title\x07\x1b[2Jname.bin"
	writeRandomFile(t, dir, name, 100)
	code, link, stderr := runCLI(t, "--server", srv.URL, "u", filepath.Join(dir, name))
	if code != 0 {
		t.Fatal(stderr)
	}
	out := filepath.Join(dir, "out")
	os.MkdirAll(out, 0o755)
	code, _, stderr = runCLI(t, "d", link, "-o", out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if strings.ContainsAny(stderr, "\x1b\x07") {
		t.Fatalf("control characters from the uploader's filename reached the terminal: %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(out, "evil]0;title[2Jname.bin")); err != nil {
		t.Fatal(err)
	}
}

func TestOversizedDownloadRefused(t *testing.T) {
	fs, srv := newFakeServer(t)
	key, _ := tmpcrypto.NewKey()
	iv, _ := tmpcrypto.NewIV()
	x, err := tmpcrypto.EncryptMetadata(key, iv, "huge.bin", upload.MaxFileSize+1)
	if err != nil {
		t.Fatal(err)
	}
	id := "12345678-1234-4123-8123-123456789abc"
	fs.mu.Lock()
	fs.headers[id] = map[string]any{"x": x, "i": hex.EncodeToString(iv[:]), "d": 10, "c": 0, "e": 3600}
	fs.files[id] = []byte{}
	fs.mu.Unlock()

	dir := t.TempDir()
	link := tmpcrypto.FormatLink(srv.URL, id, key)
	code, _, stderr := runCLI(t, "d", link, "-o", dir)
	if code != 1 || !strings.Contains(stderr, "invalid file size") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files were created: %v", entries)
	}
}

func TestUsage(t *testing.T) {
	cases := []struct {
		args []string
		code int
		want string
	}{
		{nil, 2, "missing command"},
		{[]string{"-h"}, 0, "Commands:"},
		{[]string{"nope"}, 2, "unknown command"},
		{[]string{"u"}, 2, "exactly one FILE"},
		{[]string{"u", "-t", "2d", "f"}, 2, "invalid duration"},
		{[]string{"u", "-h"}, 0, "DURATION"},
		{[]string{"d"}, 2, "exactly one LINK"},
		{[]string{"d", "https://tmpfs.it/nope"}, 2, "invalid link"},
		{[]string{"--bogus", "u"}, 2, "flag provided but not defined"},
		{[]string{"version", "x"}, 2, "no arguments"},
	}
	for _, c := range cases {
		code, _, stderr := runCLI(t, c.args...)
		if code != c.code || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d (want %d), stderr %q (want %q)", c.args, code, c.code, stderr, c.want)
		}
	}

	for _, args := range [][]string{{"--version"}, {"version"}} {
		code, stdout, _ := runCLI(t, args...)
		if code != 0 || stdout != "tmpfs-cli dev" {
			t.Errorf("%v: %d %q", args, code, stdout)
		}
	}
}
