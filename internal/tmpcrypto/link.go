package tmpcrypto

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var idRegexp = regexp.MustCompile(`\A[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}\z`)

// ValidID reports whether id has the format accepted by the server.
func ValidID(id string) bool {
	return idRegexp.MatchString(id)
}

// NewID returns a random UUID v4 (lowercase), used as file id.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// FormatLink returns the share link: <server>/file/<id>/#<base58(key)>.
func FormatLink(server, id string, k Key) string {
	return fmt.Sprintf("%s/file/%s/#%s", strings.TrimRight(server, "/"), id, EncodeKey(k))
}

// Link is a parsed share link.
type Link struct {
	Server string // scheme://host[:port]
	ID     string
	Key    Key
}

// ParseLink parses a share link produced by the webapp or the CLI.
func ParseLink(s string) (Link, error) {
	var l Link

	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return l, errors.New("invalid link: expected https://<host>/file/<id>/#<key>")
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "file" || !ValidID(parts[1]) {
		return l, errors.New("invalid link: expected https://<host>/file/<id>/#<key>")
	}
	if u.Fragment == "" {
		return l, errors.New("invalid link: the key after '#' is missing")
	}

	k, err := DecodeKey(u.Fragment)
	if err != nil {
		return l, err
	}

	l.Server = u.Scheme + "://" + u.Host
	l.ID = parts[1]
	l.Key = k
	return l, nil
}
