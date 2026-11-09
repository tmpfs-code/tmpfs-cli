package clearance

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"testing"
)

// serverVerify is a copy of the check in server/anti_abuse.go (verify).
func serverVerify(secret []byte, solution string, difficulty int) bool {
	if !regexp.MustCompile("^[a-f0-9]{16,32}$").MatchString(solution) {
		return false
	}
	solBytes, _ := hex.DecodeString(solution)
	secretSum := 0
	for _, b := range secret {
		secretSum += int(b)
	}
	h := md5.New()
	io.WriteString(h, fmt.Sprintf("%d", secretSum))
	h.Write(solBytes)
	md5Hex := hex.EncodeToString(h.Sum(nil))
	for i := 0; i < difficulty; i++ {
		if md5Hex[i] != '0' {
			return false
		}
	}
	return true
}

func TestSolveMatchesServer(t *testing.T) {
	secret := []byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0xff}
	secretHex := hex.EncodeToString(secret)

	for _, d := range []int{0, 3, 4, 5} {
		sol, err := Solve(context.Background(), secretHex, d)
		if err != nil {
			t.Fatal(err)
		}
		// always valid for the maximum server difficulty (4), whatever the challenge says
		for check := 0; check <= max(d, MinDifficulty); check++ {
			if !serverVerify(secret, sol, check) {
				t.Fatalf("difficulty %d: solution %s rejected by the server check at %d", d, sol, check)
			}
		}
	}
}

func TestSecret(t *testing.T) {
	s, err := Secret("0102ff")
	if err != nil || s != "258" {
		t.Fatalf("Secret = %q, %v", s, err)
	}
	if _, err := Secret("zz"); err == nil {
		t.Fatal("invalid hex must fail")
	}
}

func TestSolveCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Solve(ctx, "00", 12); err == nil {
		t.Fatal("a canceled context must stop the solver")
	}
}
