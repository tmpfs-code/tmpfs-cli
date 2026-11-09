// Package tmpcrypto implements the tmpfs.it encryption format, compatible with
// the webapp (webapp/src/crypto.js). See STEERING.md §4.2.
//
// Note: the format has no MAC (AES-CBC without authentication). Integrity is
// limited to the metadata check value and the file size.
package tmpcrypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

const (
	KeySize   = 32
	IVSize    = aes.BlockSize
	BlockSize = aes.BlockSize

	// MaxHeaderLen is the maximum length of the encoded upload header "h".
	MaxHeaderLen = 1024
)

// ErrWrongKey is returned when the metadata cannot be decrypted or its check
// value does not match, which means the key in the link is wrong.
var ErrWrongKey = errors.New("invalid key: the link is wrong or incomplete")

// Key is an AES-256 key.
type Key [KeySize]byte

// IV is an AES-CBC initialization vector.
type IV [IVSize]byte

// NewKey returns a random key.
func NewKey() (Key, error) {
	var k Key
	_, err := rand.Read(k[:])
	return k, err
}

// NewIV returns a random IV.
func NewIV() (IV, error) {
	var iv IV
	_, err := rand.Read(iv[:])
	return iv, err
}

// EncodeKey encodes the key as it appears in the share link fragment.
func EncodeKey(k Key) string {
	return Base58Encode(k[:])
}

// DecodeKey decodes a share link fragment into a key.
func DecodeKey(s string) (Key, error) {
	var k Key
	b, err := Base58Decode(s)
	if err != nil || len(b) != KeySize {
		return k, errors.New("invalid key in the link")
	}
	copy(k[:], b)
	return k, nil
}

// ParseIV parses a hex encoded IV.
func ParseIV(s string) (IV, error) {
	var iv IV
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != IVSize {
		return iv, errors.New("invalid iv")
	}
	copy(iv[:], b)
	return iv, nil
}

// EncryptedSize returns the size of the ciphertext for a plaintext of the
// given size (padded to a multiple of the block size).
func EncryptedSize(size int64) int64 {
	return (size + BlockSize - 1) / BlockSize * BlockSize
}

// NewEncrypter returns a CBC encrypter for the file body. Its state carries
// across calls, so the body can be encrypted chunk by chunk.
func NewEncrypter(k Key, iv IV) cipher.BlockMode {
	block, _ := aes.NewCipher(k[:]) // key size is always valid
	return cipher.NewCBCEncrypter(block, iv[:])
}

// NewDecrypter returns a CBC decrypter for the file body.
func NewDecrypter(k Key, iv IV) cipher.BlockMode {
	block, _ := aes.NewCipher(k[:])
	return cipher.NewCBCDecrypter(block, iv[:])
}

// Metadata is the decrypted sensitive metadata of a file.
type Metadata struct {
	Name string
	Size int64
}

type sensitive struct {
	N string `json:"n"`
	R string `json:"r"`
	S string `json:"s"`
	C string `json:"c"`
}

func checkValue(name, rnd string) string {
	sum := sha256.Sum256([]byte(name + rnd))
	return hex.EncodeToString(sum[:])[:6]
}

// EncryptMetadata returns the hex encoded encrypted metadata ("x").
func EncryptMetadata(k Key, iv IV, name string, size int64) (string, error) {
	name = strings.TrimSpace(name)

	// 10-29 random bytes, like the webapp
	nRnd, err := rand.Int(rand.Reader, big.NewInt(20))
	if err != nil {
		return "", err
	}
	rnd := make([]byte, 10+nRnd.Int64())
	if _, err := rand.Read(rnd); err != nil {
		return "", err
	}
	rndHex := hex.EncodeToString(rnd)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	err = enc.Encode(sensitive{
		N: name,
		R: rndHex,
		S: strconv.FormatInt(size, 16),
		C: checkValue(name, rndHex),
	})
	if err != nil {
		return "", err
	}
	plain := pkcs7Pad(bytes.TrimRight(buf.Bytes(), "\n"))

	block, _ := aes.NewCipher(k[:])
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv[:]).CryptBlocks(out, plain)
	return hex.EncodeToString(out), nil
}

// DecryptMetadata decrypts and verifies the metadata ("x").
func DecryptMetadata(k Key, iv IV, xHex string) (Metadata, error) {
	var md Metadata

	ct, err := hex.DecodeString(xHex)
	if err != nil || len(ct) == 0 || len(ct)%BlockSize != 0 {
		return md, errors.New("invalid file metadata")
	}

	block, _ := aes.NewCipher(k[:])
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(plain, ct)

	plain, err = pkcs7Unpad(plain)
	if err != nil {
		return md, ErrWrongKey
	}

	var s sensitive
	if err := json.Unmarshal(plain, &s); err != nil {
		return md, ErrWrongKey
	}
	if s.C != checkValue(s.N, s.R) {
		return md, ErrWrongKey
	}

	size, err := strconv.ParseUint(s.S, 16, 63)
	if err != nil {
		return md, fmt.Errorf("invalid file size in metadata: %q", s.S)
	}

	md.Name = s.N
	md.Size = int64(size)
	return md, nil
}

type header struct {
	X string `json:"x"`
	I string `json:"i"`
	D int    `json:"d"`
	E int    `json:"e"`
}

// EncodeHeader returns the upload header "h" sent with the last chunk.
func EncodeHeader(x string, iv IV, maxDownloads, lifetimeSec int) (string, error) {
	data, err := json.Marshal(header{X: x, I: hex.EncodeToString(iv[:]), D: maxDownloads, E: lifetimeSec})
	if err != nil {
		return "", err
	}
	h := base64.RawURLEncoding.EncodeToString(data)
	if len(h) >= MaxHeaderLen {
		return "", fmt.Errorf("upload header is too long (%d bytes), use a shorter filename", len(h))
	}
	return h, nil
}

func pkcs7Pad(b []byte) []byte {
	n := BlockSize - len(b)%BlockSize
	return append(b, bytes.Repeat([]byte{byte(n)}, n)...)
}

func pkcs7Unpad(b []byte) ([]byte, error) {
	if len(b) == 0 || len(b)%BlockSize != 0 {
		return nil, errors.New("invalid padding")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > BlockSize {
		return nil, errors.New("invalid padding")
	}
	for _, c := range b[len(b)-n:] {
		if int(c) != n {
			return nil, errors.New("invalid padding")
		}
	}
	return b[:len(b)-n], nil
}
