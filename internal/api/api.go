// Package api is the HTTP client for the tmpfs server API (STEERING.md §4).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

var (
	// ErrNotFound means the file does not exist or the link expired.
	ErrNotFound = errors.New("the link expired or does not exist")
	// ErrRateLimited means the anti-abuse system refused a new upload.
	ErrRateLimited = errors.New("too many uploads from your IP address, wait a minute and try again " +
		"(repeated attempts can get the IP address banned for an hour)")
	// ErrInvalidPromo means the promo code is unknown, already used or not applicable.
	ErrInvalidPromo = errors.New("invalid promo code (unknown or already used)")
)

// ServerError is an error reply ({"error": ...}) from the server.
type ServerError struct {
	Status  int
	Message string
}

func (e *ServerError) Error() string {
	msg := printable(e.Message, 200)
	if msg == "" {
		return fmt.Sprintf("server error (HTTP %d)", e.Status)
	}
	return fmt.Sprintf("server error (HTTP %d): %s", e.Status, msg)
}

// printable makes text received from a server safe to print on a terminal:
// the server is taken from the link, so it may be hostile. It drops control
// characters (including ANSI escape sequences' ESC) and cuts the text to max
// runes.
func printable(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			r = ' '
		}
		if n == max {
			b.WriteString("...")
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// Client talks to a tmpfs server.
type Client struct {
	baseURL   string
	userAgent string
	http      *http.Client

	// Backoff for retries: starts at RetryMin, doubles up to RetryMax, gives up
	// after RetryTotal.
	RetryMin, RetryMax, RetryTotal time.Duration

	// ChunkInterval is the minimum time between two chunk uploads, to stay
	// below the nginx rate limit (10 req/s per IP).
	ChunkInterval time.Duration

	mu        sync.Mutex
	lastChunk time.Time

	// Debugf, if set, receives debug messages.
	Debugf func(format string, args ...any)
}

// New returns a client for the server at baseURL (e.g. https://tmpfs.it).
func New(baseURL, userAgent string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid server url %q", baseURL)
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: time.Second,
		// compression stays enabled: the transport adds "Accept-Encoding: gzip"
		// (required by the server) and decompresses transparently
	}

	return &Client{
		baseURL:       strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/"),
		userAgent:     userAgent,
		http:          &http.Client{Transport: transport},
		RetryMin:      200 * time.Millisecond,
		RetryMax:      5 * time.Second,
		RetryTotal:    2 * time.Minute,
		ChunkInterval: 125 * time.Millisecond,
	}, nil
}

// BaseURL returns the server URL.
func (c *Client) BaseURL() string { return c.baseURL }

func (c *Client) debugf(format string, args ...any) {
	if c.Debugf != nil {
		c.Debugf(format, args...)
	}
}

func (c *Client) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept-Language", "en")
	return req, nil
}

type retryPolicy int

const (
	// retry only when nginx rejected the request (503): the server never saw it
	retryRejected retryPolicy = iota
	// also retry network errors: only for idempotent requests
	retryNetwork
)

// do sends a request built by mk, retrying according to policy. The caller
// must close the response body.
func (c *Client) do(ctx context.Context, policy retryPolicy, mk func() (*http.Request, error)) (*http.Response, error) {
	deadline := time.Now().Add(c.RetryTotal)
	wait := c.RetryMin

	for attempt := 1; ; attempt++ {
		req, err := mk()
		if err != nil {
			return nil, err
		}

		resp, err := c.http.Do(req)
		retry := false
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			retry = policy == retryNetwork
		case resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests:
			// rate limited by nginx
			io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			err = &ServerError{Status: resp.StatusCode, Message: "rate limited"}
			retry = true
		default:
			return resp, nil
		}

		if !retry || time.Now().Add(wait).After(deadline) {
			return nil, err
		}
		c.debugf("%s %s: attempt %d failed (%v), retrying in %v", req.Method, req.URL.Path, attempt, err, wait)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
		if wait > c.RetryMax {
			wait = c.RetryMax
		}
	}
}

// readJSON decodes a JSON reply, turning {"error": ...} into a *ServerError.
func readJSON(resp *http.Response, v any) error {
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		return &ServerError{Status: resp.StatusCode, Message: e.Error}
	}
	if resp.StatusCode != http.StatusOK {
		return &ServerError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
	}
	if v == nil {
		return nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("unexpected reply from the server: %v", err)
	}
	return nil
}

// Challenge is an anti-abuse proof-of-work challenge.
type Challenge struct {
	Secret     string `json:"s"`
	Difficulty int    `json:"d"`
	Err        int    `json:"err"`
}

// Clearance requests an upload clearance challenge.
func (c *Client) Clearance(ctx context.Context) (Challenge, error) {
	var ch Challenge

	// every request counts as an anti-abuse attempt: only retry nginx rejections
	resp, err := c.do(ctx, retryRejected, func() (*http.Request, error) {
		return c.newRequest(ctx, http.MethodPost, "/api/v1/upload/clearance", nil)
	})
	if err != nil {
		return ch, err
	}
	if err := readJSON(resp, &ch); err != nil {
		return ch, err
	}

	switch {
	case ch.Err == 2:
		return ch, ErrRateLimited
	case ch.Err == 1:
		return ch, errors.New("the server refused the upload clearance (missing client headers)")
	case ch.Err != 0:
		return ch, fmt.Errorf("the server refused the upload clearance (code %d)", ch.Err)
	case ch.Secret == "":
		return ch, errors.New("unexpected clearance reply from the server")
	}
	return ch, nil
}

// PriceReq describes an upload for pricing and promo codes.
type PriceReq struct {
	FilesizeRange  int    `json:"r"`
	MaxDownloads   int    `json:"d"`
	ExpireAfterSec int    `json:"e"`
	Currency       string `json:"c"`
}

// RedeemPromo redeems a promo code for an upload and returns the file id the
// upload must use.
func (c *Client) RedeemPromo(ctx context.Context, code string, req PriceReq) (string, error) {
	body, err := json.Marshal(struct {
		Code string   `json:"co"`
		Req  PriceReq `json:"r"`
	}{code, req})
	if err != nil {
		return "", err
	}

	// redeeming consumes the code: never retry network errors
	resp, err := c.do(ctx, retryRejected, func() (*http.Request, error) {
		return c.newRequest(ctx, http.MethodPost, "/api/v1/promo/redeem", body)
	})
	if err != nil {
		return "", err
	}

	var out struct {
		ID string `json:"id"`
	}
	if err := readJSON(resp, &out); err != nil {
		var se *ServerError
		if errors.As(err, &se) && se.Status == http.StatusBadRequest {
			return "", ErrInvalidPromo
		}
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("unexpected promo reply from the server")
	}
	return out.ID, nil
}

// Chunk is one upload request.
type Chunk struct {
	ID        string
	From, To  int64  // ciphertext offsets
	Data      []byte // ciphertext, len == To-From
	Clearance string // first chunk only
	Header    string // last chunk only
}

// UploadChunk uploads one chunk. Errors reported by the server are fatal: the
// server deletes the whole upload when a chunk fails.
func (c *Client) UploadChunk(ctx context.Context, ch Chunk) error {
	c.pace(ctx)

	path := fmt.Sprintf("/api/v1/upload/%s/%d/%d", ch.ID, ch.From, ch.To)
	if ch.Header != "" {
		path += "?h=" + url.QueryEscape(ch.Header)
	}

	// The first chunk carries the clearance: the server verifies it again on
	// every attempt, so an ambiguous retry could exceed the anti-abuse limits
	// and get the IP banned. Retry it only when nginx rejected it.
	policy := retryNetwork
	if ch.Clearance != "" {
		policy = retryRejected
	}

	resp, err := c.do(ctx, policy, func() (*http.Request, error) {
		req, err := c.newRequest(ctx, http.MethodPost, path, ch.Data)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		if ch.Clearance != "" {
			req.Header.Set("x-clearance", ch.Clearance)
		}
		return req, nil
	})
	if err != nil {
		return err
	}
	return readJSON(resp, nil)
}

// pace waits so that chunk uploads are at least ChunkInterval apart.
func (c *Client) pace(ctx context.Context) {
	c.mu.Lock()
	wait := time.Until(c.lastChunk.Add(c.ChunkInterval))
	c.mu.Unlock()
	if wait > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
	c.mu.Lock()
	c.lastChunk = time.Now()
	c.mu.Unlock()
}

// FileMetadata is the public part of a file's metadata.
type FileMetadata struct {
	X string `json:"x"` // encrypted sensitive metadata (hex)
	I string `json:"i"` // iv (hex)
	D int    `json:"d"` // max downloads
	C int64  `json:"c"` // created at (unix)
	E int    `json:"e"` // lifetime in seconds
}

// FileHeader is the reply of the download header endpoint.
type FileHeader struct {
	FM            FileMetadata `json:"fm"`
	DownloadsLeft int          `json:"dl"`
	SecondsLeft   int64        `json:"tl"`
}

// DownloadHeader fetches a file's metadata. It does not use up a download.
func (c *Client) DownloadHeader(ctx context.Context, id string) (FileHeader, error) {
	var h FileHeader

	resp, err := c.do(ctx, retryNetwork, func() (*http.Request, error) {
		return c.newRequest(ctx, http.MethodGet, "/api/v1/download/"+id+"/header", nil)
	})
	if err != nil {
		return h, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return h, ErrNotFound
	}
	if err := readJSON(resp, &h); err != nil {
		return h, err
	}
	return h, nil
}

// Download starts downloading a file's ciphertext. It uses up one download.
// The caller must close the returned body.
func (c *Client) Download(ctx context.Context, id string) (io.ReadCloser, int64, error) {
	// a network error may have reached the server (and used up a download):
	// only retry nginx rejections
	resp, err := c.do(ctx, retryRejected, func() (*http.Request, error) {
		return c.newRequest(ctx, http.MethodGet, "/api/v1/download/"+id, nil)
	})
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		err := readJSON(resp, nil)
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusInternalServerError {
			// the server replies 500 when the metadata is gone (expired link)
			return nil, 0, fmt.Errorf("%w (%v)", ErrNotFound, err)
		}
		return nil, 0, err
	}

	size := resp.ContentLength
	if size < 0 {
		if v, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); err == nil {
			size = v
		}
	}
	return resp.Body, size, nil
}
