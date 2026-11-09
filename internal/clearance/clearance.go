// Package clearance solves the server's anti-abuse proof-of-work challenge
// (see webapp/src/upload-clearance.js and server/anti_abuse.go).
package clearance

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MinDifficulty is the difficulty every solution is computed for, at least.
// The server checks the solution against a difficulty that depends on the
// number of attempts made at verification time (3 or 4 leading zeros), which
// can be higher than the one in the challenge if the first chunk is retried.
// A wrong solution bans the IP for an hour, so always solve for the maximum:
// a hash with 4 leading zeros also satisfies difficulty 3.
const MinDifficulty = 4

// Secret returns the string hashed before the solution: the decimal sum of
// the bytes of the hex-decoded challenge secret.
func Secret(secretHex string) (string, error) {
	b, err := hex.DecodeString(secretHex)
	if err != nil || len(b) == 0 {
		return "", errors.New("invalid clearance challenge secret")
	}
	sum := 0
	for _, c := range b {
		sum += int(c)
	}
	return strconv.Itoa(sum), nil
}

// Check reports whether solution (hex) satisfies the challenge at the given
// difficulty, with the same logic as the server.
func Check(secretHex, solutionHex string, difficulty int) bool {
	secret, err := Secret(secretHex)
	if err != nil {
		return false
	}
	sol, err := hex.DecodeString(solutionHex)
	if err != nil || len(sol) < 8 || len(sol) > 16 {
		return false
	}
	h := md5.New()
	h.Write([]byte(secret))
	h.Write(sol)
	return strings.HasPrefix(hex.EncodeToString(h.Sum(nil)), strings.Repeat("0", difficulty))
}

// Solve finds a 16-byte solution (hex encoded) for the challenge.
func Solve(ctx context.Context, secretHex string, difficulty int) (string, error) {
	if difficulty < 0 || difficulty > 16 {
		return "", fmt.Errorf("unsupported clearance difficulty %d", difficulty)
	}
	if difficulty < MinDifficulty {
		difficulty = MinDifficulty
	}

	secret, err := Secret(secretHex)
	if err != nil {
		return "", err
	}

	// a hash has `difficulty` leading zero nibbles
	fullBytes, halfByte := difficulty/2, difficulty%2 == 1

	var sol [16]byte
	h := md5.New()
	var sum []byte
	for i := 0; ; i++ {
		if i%4096 == 0 && ctx.Err() != nil {
			return "", ctx.Err()
		}
		if _, err := rand.Read(sol[:]); err != nil {
			return "", err
		}
		h.Reset()
		h.Write([]byte(secret))
		h.Write(sol[:])
		sum = h.Sum(sum[:0])

		ok := true
		for j := 0; j < fullBytes; j++ {
			if sum[j] != 0 {
				ok = false
				break
			}
		}
		if ok && halfByte && sum[fullBytes]>>4 != 0 {
			ok = false
		}
		if ok {
			solution := hex.EncodeToString(sol[:])
			// never send an unverified solution: a wrong one gets the IP banned
			if !Check(secretHex, solution, difficulty) {
				return "", errors.New("internal error: clearance solution failed verification")
			}
			return solution, nil
		}
	}
}
