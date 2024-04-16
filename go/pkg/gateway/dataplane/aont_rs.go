package dataplane

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"

	"github.com/cloud9-tools/go-galoisfield"
)

// Galoas field for all-or-nothing transform
var (
	GF   = galoisfield.Default
	GF_a = GF.Exp(85)
	GF_b = GF.Exp(170)
)



func AONT_RSEncode(bytes []byte) []byte{
	// Use the notation of the paper AONT-RS


	// Load your secret key from a safe place and reuse it across multiple
	// NewCipher calls. (Obviously don't use this example key for anything
	// real.) If you want to convert a passphrase to a key, use a suitable
	// package like bcrypt or scrypt.
	key, _ := hex.DecodeString("6368616e676520746869732070617373")
	plaintext := []byte("some plaintext")

	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}

	// The IV needs to be unique, but not secure. Therefore it's common to
	// include it at the beginning of the ciphertext.
	ciphertext := make([]byte, len(plaintext))
	// iv := ciphertext[:aes.BlockSize]
	iv = make([]bytes, aes.BlockSize)
	// if _, err := io.ReadFull(rand.Reader, iv); err != nil {
	// 	panic(err)
	// }

	stream := cipher.NewCTR(block, iv)
	stream.XORKeyStream(ciphertext, plaintext)
	fmt.Printf("%s\n", ciphertext)

	// It's important to remember that ciphertexts must be authenticated
	// (i.e. by using crypto/hmac) as well as being encrypted in order to
	// be secure.

	// CTR mode is the same for both encryption and decryption, so we can
	// also decrypt that ciphertext with NewCTR.
	//
	// plaintext2 := make([]byte, len(plaintext))
	// stream = cipher.NewCTR(block, iv)
	// stream.XORKeyStream(plaintext2, ciphertext[aes.BlockSize:])

	fmt.Printf("%s\n", plaintext2)

	// processe block of 16 bytes

}

func AONT_RSDecode(bytes []byte) []byte{
}

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
	cum := byte(0)
	for i := 0; i < n; i++ {
		cum = GF.Add(cum, bytes[i])
	}
	if n%2 == 0 {
		cum = GF.Mul(cum, GF_a)
	} else {
		cum = GF.Mul(cum, GF_b)
	}
	for i := 0; i < n-1; i++ {
		bytes[i] = GF.Add(cum, bytes[i])
	}
	bytes[n-1] = cum
	return bytes
}
