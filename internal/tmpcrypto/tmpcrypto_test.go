package tmpcrypto

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// Fixtures produced with the webapp's own libraries (crypto-js, bs58,
// int64-buffer) using the same calls as webapp/src/crypto.js.
const (
	fxKeyHex     = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	fxIVHex      = "a0a1a2a3a4a5a6a7a8a9aaabacadaeaf"
	fxName       = "résumé <final> & co.pdf"
	fxSize       = 123456789
	fxX          = "0591c05fc391487c7f50c766a9fa4be047c4b1ffa9f0d9d44f12a7366a7f056738da31449ca12215d8e76e475d67aa7ebbb4acf552649556f0c2faa921df050856d1ef771cf8015413faf8671a4bdba40de36ff637280f607f6d9341077b2929"
	fxKeyB58     = "1thX6LZfHDZZKUs92febYZhYRcXddmzfzF2NvTkPNE"
	fxZeroKeyB58 = "11111111111111111111111111111111"
	fxBodyPlain  = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f202122232425262728292a2b2c2d2e2f"
	fxBodyCipher = "224c27f4ba378b27d3d6888adced64425830cb2973f008c46e64c4e1ca612106f9c530e03496918b5ccda474a2fabf53"
)

func fxKeyIV(t *testing.T) (Key, IV) {
	t.Helper()
	var k Key
	b, _ := hex.DecodeString(fxKeyHex)
	copy(k[:], b)
	iv, err := ParseIV(fxIVHex)
	if err != nil {
		t.Fatal(err)
	}
	return k, iv
}

func TestBase58WebappCompat(t *testing.T) {
	k, _ := fxKeyIV(t)
	if got := EncodeKey(k); got != fxKeyB58 {
		t.Fatalf("EncodeKey = %s, want %s", got, fxKeyB58)
	}
	if got := Base58Encode(make([]byte, 32)); got != fxZeroKeyB58 {
		t.Fatalf("zero key = %s, want %s", got, fxZeroKeyB58)
	}
	dk, err := DecodeKey(fxKeyB58)
	if err != nil || dk != k {
		t.Fatalf("DecodeKey = %x, %v", dk, err)
	}
	if _, err := DecodeKey(fxZeroKeyB58); err != nil {
		t.Fatalf("DecodeKey(zero) = %v", err)
	}
}

func TestBase58RoundTrip(t *testing.T) {
	for i := 0; i < 200; i++ {
		b := make([]byte, i%40)
		rand.Read(b)
		if i%5 == 0 && len(b) > 2 {
			b[0], b[1] = 0, 0
		}
		s := Base58Encode(b)
		got, err := Base58Decode(s)
		if len(b) == 0 {
			if err == nil {
				t.Fatal("decoding an empty string must fail")
			}
			continue
		}
		if err != nil || !bytes.Equal(got, b) {
			t.Fatalf("round trip %x -> %s -> %x, %v", b, s, got, err)
		}
	}
	for _, bad := range []string{"0abc", "Iabc", "O", "l", "abc def"} {
		if _, err := Base58Decode(bad); err == nil {
			t.Errorf("Base58Decode(%q) must fail", bad)
		}
	}
	if _, err := DecodeKey("abc"); err == nil {
		t.Error("a short key must be rejected")
	}
}

func TestDecryptWebappMetadata(t *testing.T) {
	k, iv := fxKeyIV(t)
	md, err := DecryptMetadata(k, iv, fxX)
	if err != nil {
		t.Fatal(err)
	}
	if md.Name != fxName || md.Size != fxSize {
		t.Fatalf("got %+v", md)
	}

	// wrong key
	k2 := k
	k2[5] ^= 1
	if _, err := DecryptMetadata(k2, iv, fxX); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("wrong key: err = %v, want ErrWrongKey", err)
	}
}

func TestMetadataRoundTrip(t *testing.T) {
	k, _ := NewKey()
	iv, _ := NewIV()
	for _, name := range []string{"a", "  spaced name.txt  ", fxName, strings.Repeat("x", 300), "日本語.bin"} {
		for _, size := range []int64{1, 15, 16, 17, 2_000_000_000} {
			x, err := EncryptMetadata(k, iv, name, size)
			if err != nil {
				t.Fatal(err)
			}
			md, err := DecryptMetadata(k, iv, x)
			if err != nil {
				t.Fatal(err)
			}
			if md.Name != strings.TrimSpace(name) || md.Size != size {
				t.Fatalf("got %+v, want %q %d", md, name, size)
			}
		}
	}
}

func TestBodyWebappCompat(t *testing.T) {
	k, iv := fxKeyIV(t)
	plain, _ := hex.DecodeString(fxBodyPlain)
	want, _ := hex.DecodeString(fxBodyCipher)

	// encrypted in two chunks: CBC state must carry over
	got := append([]byte(nil), plain...)
	enc := NewEncrypter(k, iv)
	enc.CryptBlocks(got[:16], got[:16])
	enc.CryptBlocks(got[16:], got[16:])
	if !bytes.Equal(got, want) {
		t.Fatalf("ciphertext = %x, want %x", got, want)
	}

	dec := NewDecrypter(k, iv)
	dec.CryptBlocks(got[:32], got[:32])
	dec.CryptBlocks(got[32:], got[32:])
	if !bytes.Equal(got, plain) {
		t.Fatalf("plaintext = %x", got)
	}
}

func TestEncryptedSize(t *testing.T) {
	for in, want := range map[int64]int64{1: 16, 15: 16, 16: 16, 17: 32, 1_800_000: 1_800_000, 2_000_000_000: 2_000_000_000} {
		if got := EncryptedSize(in); got != want {
			t.Errorf("EncryptedSize(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestHeader(t *testing.T) {
	iv, _ := NewIV()
	h, err := EncodeHeader("abcd", iv, 10, 86400)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(h, "+/=") {
		t.Fatalf("header must be unpadded base64url: %s", h)
	}
	if _, err := EncodeHeader(strings.Repeat("a", 1000), iv, 10, 86400); err == nil {
		t.Fatal("an overlong header must be rejected")
	}
}

func TestLink(t *testing.T) {
	k, _ := NewKey()
	id, err := NewID()
	if err != nil || !ValidID(id) {
		t.Fatalf("NewID = %q, %v", id, err)
	}

	link := FormatLink("https://tmpfs.it/", id, k)
	if want := "https://tmpfs.it/file/" + id + "/#" + EncodeKey(k); link != want {
		t.Fatalf("FormatLink = %s, want %s", link, want)
	}

	l, err := ParseLink("  " + link + "\n")
	if err != nil || l.Server != "https://tmpfs.it" || l.ID != id || l.Key != k {
		t.Fatalf("ParseLink = %+v, %v", l, err)
	}

	// webapp links without the trailing slash, local servers with a port
	l, err = ParseLink("http://localhost:8080/file/" + id + "#" + EncodeKey(k))
	if err != nil || l.Server != "http://localhost:8080" {
		t.Fatalf("ParseLink(local) = %+v, %v", l, err)
	}

	for _, bad := range []string{
		"",
		"tmpfs.it/file/" + id + "/#" + EncodeKey(k),
		"ftp://tmpfs.it/file/" + id + "/#" + EncodeKey(k),
		"https://tmpfs.it/file/" + id + "/",
		"https://tmpfs.it/file/not-an-id/#" + EncodeKey(k),
		"https://tmpfs.it/x/" + id + "/#" + EncodeKey(k),
		"https://tmpfs.it/file/" + id + "/#abc",
	} {
		if _, err := ParseLink(bad); err == nil {
			t.Errorf("ParseLink(%q) must fail", bad)
		}
	}
}
