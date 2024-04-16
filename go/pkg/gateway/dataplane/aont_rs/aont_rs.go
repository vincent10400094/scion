package main

import (
	"crypto/aes"
	// "crypto/cipher"
	"crypto/rand"
	"fmt"
	// "encoding/hex"
	"encoding/binary"
	"github.com/andreburgaud/crypt2go/padding"
	"crypto/sha256"
)



func AONT_Encode(message []byte) []byte{
	// Use the notation of the paper AONT-RS
	key := make([]byte, 2*aes.BlockSize)
	_, err := rand.Read(key)
	if err != nil {
		panic(err)
	}

	// padding message

	aes_block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}


	// Padding message into multiple of 16 Using PKC#7
	padder := padding.NewPkcs7Padding(16)
	p_message, err := padder.Pad(message)
	if err != nil{
		panic(err)
	}
	s := len(p_message) / aes.BlockSize

	// canary block, the canary block is set to be zeros
	// | p_message | canary(16 bytes) | key (32 bytes) |
	// d0, d1,...ds-1, ds(canary), ds+1, ds+2
	encoded_text := make([]byte, len(p_message) + 3*aes.BlockSize)
	copy(encoded_text, p_message)


	// encoding
	for i:=0; i<=s; i++ {
		mask := make([]byte, aes.BlockSize)
		binary.BigEndian.PutUint64(mask, uint64(i+1))
		if err != nil {
			panic(err)
		}

		aes_block.Encrypt(mask,mask)
		for j:= 0; j<aes.BlockSize; j++ {
			encoded_text[i*aes.BlockSize+j] ^= mask[j]
		}
	}
	// compute c_{s+1}
	h := sha256.New()
	h.Write(encoded_text[:(s+1)*aes.BlockSize])
	copy(encoded_text[(s+1)*aes.BlockSize:], h.Sum(nil))

	for j:=0; j < 2*aes.BlockSize; j++ {
		encoded_text[(s+1)*aes.BlockSize+j] ^= key[j]
	}
	return encoded_text

}

func AONT_Decode(encoded_text []byte) []byte {
	s := len(encoded_text) / aes.BlockSize - 3
	h := sha256.New()
	h.Write(encoded_text[:(s+1)*aes.BlockSize])
	hash_document := h.Sum(nil)
	for j:=0; j < 2*aes.BlockSize; j++ {
		encoded_text[(s+1)*aes.BlockSize+j] ^= hash_document[j]
	}
	aes_block, err := aes.NewCipher(encoded_text[(s+1)*aes.BlockSize:])
	if err != nil {
		panic(err)
	}


	// encoding
	for i:=0; i<=s; i++ {
		mask := make([]byte, aes.BlockSize)
		binary.BigEndian.PutUint64(mask, uint64(i+1))
		if err != nil {
			panic(err)
		}

		aes_block.Encrypt(mask,mask)
		for j:= 0; j<aes.BlockSize; j++ {
			encoded_text[i*aes.BlockSize+j] ^= mask[j]
		}
	}

	padder := padding.NewPkcs7Padding(16)
	message, err := padder.Unpad(encoded_text[:s*aes.BlockSize])
	if err != nil{
		panic(err)
	}

	return message

}

func main(){
	plaintext := []byte("some plaintext")
	encoded := AONT_Encode(plaintext)
	fmt.Println(encoded)
	fmt.Println(plaintext)
	fmt.Println(AONT_Decode(encoded))
}

// func AONT_RSDecode(bytes []byte) []byte{
// }

// func AONTEncode(bytes []byte) []byte {
// 	n := len(bytes)
// 	if n <= 1 {
// 		// AONT is not applied
// 		return bytes
// 	}
//
// 	// Default: GF(256)
// 	// a in GF(256) where a^2 = a + 1
// 	// AONT encoder is actually a matrix muliplication:
// 	// | y1 |   | 1 0 ... 0 1 | | x1 |
// 	// | y2 |   | 0 1 ... 0 1 | | x2 |
// 	// | y3 | = | ... ... ... | | x3 |
// 	// | .. |   | 0 0 ... 1 1 | | .. |
// 	// | yn |   | 1 1 ... 1 a | | xn |
// 	cum := GF.Mul(GF_a, bytes[n-1])
// 	for i := 0; i < n-1; i++ {
// 		cum = GF.Add(cum, bytes[i])
// 		bytes[i] = GF.Add(bytes[i], bytes[n-1])
// 	}
// 	bytes[n-1] = cum
// 	return bytes
// }
//
// func AONTDecode(bytes []byte) []byte {
// 	n := len(bytes)
// 	if n <= 1 {
// 		// AONT is not applied
// 		return bytes
// 	}
//
// 	// Default: GF(256)
// 	// a, b in GF(256) where b = a^2 = a + 1
// 	// Similarly, we also have a = b^2 = b + 1
// 	// AONT decoder is actually a matrix muliplication:
// 	// Case 1: n is even
// 	// | x1 |   | b a ... a a | | y1 |   | a a ... a a | | y1 |   | y1 |
// 	// | x2 |   | a b ... a a | | y2 |   | a a ... a a | | y2 |   | y2 |
// 	// | x3 | = | ... ... ... | | y3 | = | ... ... ... | | y3 | + | y3 |
// 	// | .. |   | a a ... b a | | .. |   | a a ... a a | | .. |   | .. |
// 	// | xn |   | a a ... a a | | yn |   | a a ... a a | | yn |   | 0  |
// 	// Case 2: n is odd
// 	// | x1 |   | a b ... b b | | y1 |   | b b ... b b | | y1 |   | y1 |
// 	// | x2 |   | b a ... b b | | y2 |   | b b ... b b | | y2 |   | y2 |
// 	// | x3 | = | ... ... ... | | y3 | = | ... ... ... | | y3 | + | y3 |
// 	// | .. |   | b b ... a b | | .. |   | b b ... b b | | .. |   | .. |
// 	// | xn |   | b b ... b b | | yn |   | b b ... b b | | yn |   | 0  |
// 	cum := byte(0)
// 	for i := 0; i < n; i++ {
// 		cum = GF.Add(cum, bytes[i])
// 	}
// 	if n%2 == 0 {
// 		cum = GF.Mul(cum, GF_a)
// 	} else {
// 		cum = GF.Mul(cum, GF_b)
// 	}
// 	for i := 0; i < n-1; i++ {
// 		bytes[i] = GF.Add(cum, bytes[i])
// 	}
// 	bytes[n-1] = cum
// 	return bytes
// }
