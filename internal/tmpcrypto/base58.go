package tmpcrypto

import (
	"errors"
	"math/big"
)

// Bitcoin base58 alphabet, as used by the npm "bs58" package in the webapp.
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var base58Index = func() [256]int {
	var idx [256]int
	for i := range idx {
		idx[i] = -1
	}
	for i := 0; i < len(base58Alphabet); i++ {
		idx[base58Alphabet[i]] = i
	}
	return idx
}()

var bigRadix = big.NewInt(58)

// Base58Encode encodes b; each leading zero byte becomes a leading '1'.
func Base58Encode(b []byte) string {
	zeros := 0
	for zeros < len(b) && b[zeros] == 0 {
		zeros++
	}

	n := new(big.Int).SetBytes(b)
	mod := new(big.Int)
	out := make([]byte, 0, len(b)*138/100+1)
	for n.Sign() > 0 {
		n.DivMod(n, bigRadix, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for i := 0; i < zeros; i++ {
		out = append(out, base58Alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// Base58Decode decodes s; each leading '1' becomes a leading zero byte.
func Base58Decode(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("empty base58 string")
	}

	zeros := 0
	for zeros < len(s) && s[zeros] == base58Alphabet[0] {
		zeros++
	}

	n := new(big.Int)
	for i := 0; i < len(s); i++ {
		v := base58Index[s[i]]
		if v < 0 {
			return nil, errors.New("invalid base58 character")
		}
		n.Mul(n, bigRadix)
		n.Add(n, big.NewInt(int64(v)))
	}

	body := n.Bytes()
	out := make([]byte, zeros+len(body))
	copy(out[zeros:], body)
	return out, nil
}
