package dataplane

import (
	"testing"
	"github.com/stretchr/testify/assert"
	"fmt"
)

func TestAONT_RS(t *testing.T){
	packet := []byte{
		// SIG header
		0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 0,
		// IPv4 header
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19,
		20, 21, 22, 23, 24, 25, 26, 27, 28,
	}
	shards := AONT_RS_Encode(packet, 2, 1)

	// t.Logf("Length of each shard %v\n", len(shards[0]))
	fmt.Printf("Length of each shard %v\n", len(shards[0]))
	// t.Log(shards)
	packet_dup := AONT_RS_Decode(shards, 2, 1);
	// t.Log(packet_dup)
	assert.EqualValues(t, packet_dup ,packet)
}
