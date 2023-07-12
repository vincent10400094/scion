package dataplane

import (
	"github.com/cloud9-tools/go-galoisfield"
)

// Galoas field for all-or-nothing transform
var (
	GF   = galoisfield.Default
	GF_a = GF.Exp(85)
)

func AONTEncode(bytes []byte) []byte {
	n := len(bytes)
	if n <= 1 {
		// AONT is not applied
		return bytes
	}

	// Default: GF(256)
	// a in GF(256) where a^2 = a + 1
	// AONT encoder is actually a matrix muliplication:
	// | y1 |   | 1 0 ... 0 1 | | x1 |
	// | y2 |   | 0 1 ... 0 1 | | x2 |
	// | y3 | = | ... ... ... | | x3 |
	// | .. |   | 0 0 ... 1 1 | | .. |
	// | yn |   | 1 1 ... 1 a | | xn |
	cum := GF.Mul(GF_a, bytes[n-1])
	for i := 0; i < n-1; i++ {
		cum = GF.Add(cum, bytes[i])
		bytes[i] = GF.Add(bytes[i], bytes[n-1])
	}
	bytes[n-1] = cum
	return bytes
}

func AONTDecode(bytes []byte) []byte {
	n := len(bytes)
	if n <= 1 {
		// AONT is not applied
		return bytes
	}

	// Default: GF(256)
	// a, b in GF(256) where b = a^2 = a + 1
	// Similarly, we also have a = b^2 = b + 1
	// AONT decoder is actually a matrix muliplication:
	// Case 1: n is even
	// | x1 |   | b a ... a a | | y1 |   | a a ... a a | | y1 |   | y1 |
	// | x2 |   | a b ... a a | | y2 |   | a a ... a a | | y2 |   | y2 |
	// | x3 | = | ... ... ... | | y3 | = | ... ... ... | | y3 | + | y3 |
	// | .. |   | a a ... b a | | .. |   | a a ... a a | | .. |   | .. |
	// | xn |   | a a ... a a | | yn |   | a a ... a a | | yn |   | 0  |
	// Case 2: n is odd
	// | x1 |   | a b ... b b | | y1 |   | b b ... b b | | y1 |   | y1 |
	// | x2 |   | b a ... b b | | y2 |   | b b ... b b | | y2 |   | y2 |
	// | x3 | = | ... ... ... | | y3 | = | ... ... ... | | y3 | + | y3 |
	// | .. |   | b b ... a b | | .. |   | b b ... b b | | .. |   | .. |
	// | xn |   | b b ... b b | | yn |   | b b ... b b | | yn |   | 0  |

	GF := galoisfield.Default
	a := GF.Exp(85)
	b := GF.Exp(170)
	ret := make([]byte, n)
	for i := 0; i < n; i++ {
		ret[n-1] = GF.Add(ret[n-1], bytes[i])
	}
	if n%2 == 0 {
		ret[n-1] = GF.Mul(ret[n-1], a)
	} else {
		ret[n-1] = GF.Mul(ret[n-1], b)
	}
	for i := 0; i < n-1; i++ {
		ret[i] = GF.Add(ret[n-1], bytes[i])
	}
	return ret
}
