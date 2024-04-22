package dataplane

import (
	"crypto/aes"
	// "crypto/cipher"
	"crypto/rand"
	"fmt"
	// "encoding/hex"
	"encoding/binary"
	"github.com/andreburgaud/crypt2go/padding"
	"crypto/sha256"
	"github.com/klauspost/reedsolomon"
	"reflect"

)

var padder = padding.NewPkcs7Padding(16)
var canary_block []byte = []byte{0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15}

func AONT_RS_Encode(message []byte, nb_data_shards int, nb_parties_shards int ) [][]byte{
	return RS_Encode(AONT_Encode(message), nb_data_shards, nb_parties_shards)
}

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
	copy(encoded_text[len(p_message):], canary_block)


	// encoding
	for i:=0; i<=s; i++ {
		mask := make([]byte, aes.BlockSize)
		binary.BigEndian.PutUint64(mask, uint64(i+1))
		// if err != nil {
		// 	panic(err)
		// }

		aes_block.Encrypt(mask,mask)
		for j:= 0; j<aes.BlockSize; j++ {
			encoded_text[i*aes.BlockSize+j] ^= mask[j]
		}
	}
	// compute c_{s+1}
	var hash_value = sha256.Sum256(encoded_text[:(s+1)*aes.BlockSize])
	copy(encoded_text[(s+1)*aes.BlockSize:], hash_value[:])

	for j:=0; j < 2*aes.BlockSize; j++ {
		encoded_text[(s+1)*aes.BlockSize+j] ^= key[j]
	}
	return encoded_text
}

func RS_Encode(encoded_text []byte, nb_data_shards int, nb_parties_shards int) [][]byte{
	rs_enc, _ :=  reedsolomon.New(nb_data_shards,nb_parties_shards)
	shards, _ := rs_enc.Split(encoded_text)
	_ = rs_enc.Encode(shards)
	ok, _ := rs_enc.Verify(shards)
	if ok {
		fmt.Println("ok")
	}
	return shards
}



func AONT_Decode(encoded_text []byte) []byte {
	s := len(encoded_text) / aes.BlockSize - 3
	hash_document := sha256.Sum256(encoded_text[:(s+1)*aes.BlockSize])
	for j:=0; j < 2*aes.BlockSize; j++ {
		encoded_text[(s+1)*aes.BlockSize+j] ^= hash_document[j]
	}
	aes_block, err := aes.NewCipher(encoded_text[(s+1)*aes.BlockSize:])
	if err != nil {
		panic(err)
	}


	// decoding
	for i:=0; i<=s; i++ {
		mask := make([]byte, aes.BlockSize)
		binary.BigEndian.PutUint64(mask, uint64(i+1))
		// if err != nil {
		// 	panic(err)
		// }

		aes_block.Encrypt(mask,mask)
		for j:= 0; j<aes.BlockSize; j++ {
			encoded_text[i*aes.BlockSize+j] ^= mask[j]
		}
	}

	// veryfy canary
	if !reflect.DeepEqual(encoded_text[s*aes.BlockSize:(s+1)*aes.BlockSize],
		canary_block){
		fmt.Println("Canary integrety not good")
	}

	message, err := padder.Unpad(encoded_text[:s*aes.BlockSize])
	if err != nil{
		panic(err)
	}

	return message

}

// func RS_Decode(encoded_shard [][]byte) []byte{
// 	return "
// }

func main(){
	plaintext := []byte("some plaintext")
	encoded := AONT_Encode(plaintext)
	fmt.Println(encoded)
	fmt.Println(plaintext)
	fmt.Println(AONT_Decode(encoded))
}
