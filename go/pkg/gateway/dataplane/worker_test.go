// Copyright 2019 Anapaya Systems
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dataplane

import (
	"context"
	"net"
	"fmt"
	"testing"
	"encoding/binary"

	"github.com/stretchr/testify/assert"

	"github.com/scionproto/scion/go/lib/ringbuf"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/xtest"
)

type MockTun struct {
	packets [][]byte
}

func (mt *MockTun) Read(p []byte) (n int, err error) {
	return n, nil
}

func (mt *MockTun) Write(p []byte) (n int, err error) {
	mt.packets = append(mt.packets, p)
	return n, nil
}

func (mt *MockTun) Close() error {
	return nil
}

func (mt *MockTun) AssertPacket(t *testing.T, expected []byte) {
	assert.NotEqual(t, 0, len(mt.packets))
	if len(mt.packets) != 0 {
		assert.Equal(t, expected, mt.packets[0])
		mt.packets = mt.packets[1:]
	}
}

func (mt *MockTun) AssertDone(t *testing.T) {
	assert.Equal(t, 0, len(mt.packets))
}

func SendFrame(t *testing.T, w *worker, data []byte) {
	frames := make(ringbuf.EntryList, 1)
	n := newFrameBufs(frames)
	assert.Equal(t, 1, n)
	f := frames[0].(*frameBuf)
	copy(f.raw, data)
	f.frameLen = len(data)
	w.processFrame(context.Background(), f)
}

func Split_aont_rs(frame []byte, data_shards int, parity_shards int) [][]byte{
	payload := frame[hdrLen:]
	fmt.Printf("[worker_test Split_aont_rs] payload: %v\n", payload)
	splits := AONT_RS_Encode(payload, data_shards, parity_shards);

	return splits

}

func Split(data []byte, n int) [][]byte {
	splits := make([][]byte, n)
	dataLen := (len(data)+n-1-hdrLen)/n
	for i := 0; i < n; i++ {
		splits[i] = make([]byte, hdrLen+dataLen)
		copy(splits[i], data[:hdrLen-1])
		splits[i][hdrLen-1] = uint8(i)
	}
	now := hdrLen
	for i := 0; i < dataLen - 1; i++ {
		currBytes := AONTEncode(data[now:now+n])
		for j := 0; j < n; j++ {
			splits[j][hdrLen+i] = currBytes[j]
		}
		now += n
	}
	// The left bytes, maybe less than n bytes
	// just pad 0
	currBytes := make([]byte, n)
	copy(currBytes, data[now:])
	currBytes = AONTEncode(currBytes)
	for i := 0; i < n; i++ {
		splits[i][hdrLen+dataLen-1] = currBytes[i]
	}
	return splits
}

func encodeFrame(pkt []byte, index uint16, streamID uint32, pathId uint8,
	sessionID uint8, seq uint64 ) []byte {
	frame := make([]byte, len(pkt)+hdrLen)
	copy(frame[hdrLen:], pkt)
	// Write the header.
	frame[versionPos] = 0
	frame[sessPos] = sessionID
	binary.BigEndian.PutUint16(frame[indexPos:indexPos+2], index)
	binary.BigEndian.PutUint32(frame[streamPos:streamPos+4], streamID&0xfffff)
	// The last 8 bits of sequence number is used as path ID.
	// seq := s.seq
	binary.BigEndian.PutUint64(frame[seqPos:seqPos+8], (seq<<8)+uint64(pathId))

	return frame
}

func SplitAndSend_aont_rs(t *testing.T, w *worker, frame []byte){
	data_shards := 2
	parity_shards := 1
	splits := Split_aont_rs(frame, data_shards, parity_shards)

	var sessionID uint8 = frame[sessPos]
	index := binary.BigEndian.Uint16(frame[indexPos : indexPos+2])
	streamID := binary.BigEndian.Uint32(frame[streamPos : streamPos+4])
	seq := binary.BigEndian.Uint64(frame[seqPos: seqPos+8]);


	for i := 0; i<data_shards + parity_shards; i++{
		SendFrame(t, w, encodeFrame(splits[i], index, streamID, uint8(i), sessionID, seq))
	}
}

func SplitAndSend(t *testing.T, w *worker, data []byte) {
	n := 2
	splits := Split(data, n)

	for i := 0; i < n; i++ {
		SendFrame(t, w, splits[i])
	}
}

func TestParsing(t *testing.T) {
	addr := &snet.UDPAddr{
		IA: xtest.MustParseIA("1-ff00:0:300"),
		Host: &net.UDPAddr{
			IP:   net.IP{192, 168, 1, 1},
			Port: 80,
		},
	}
	mt := &MockTun{}
	w := newWorker(addr, 1, mt, IngressMetrics{})

	// Single frame with a single IPv4 packet inside.
	SplitAndSend_aont_rs(t, w, []byte{
		// SIG frame header.
		0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 0,
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		101, 102, 103,

		// 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0,
		// 64 0 0 23 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0
		// 101 102 103
	})

	t.Log(mt)
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		101, 102, 103,
	})
	mt.AssertDone(t)

	// Single frame with a single IPv6 packet inside.
	SplitAndSend_aont_rs(t, w, []byte{
		// SIG frame header.
		0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 2, 0,
		// IPv6 header.
		0x60, 0, 0, 0, 0, 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		101, 102, 103,
	})
	mt.AssertPacket(t, []byte{
		// IPv6 header.
		0x60, 0, 0, 0, 0, 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		101, 102, 103,
	})
	mt.AssertDone(t)

	// Single frame with two packets inside.
	SplitAndSend_aont_rs(t, w, []byte{
		// SIG frame header.
		0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 3, 0,
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		101, 102, 103,
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		201, 202, 203,
	})
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		101, 102, 103,
	})
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		201, 202, 203,
	})
	mt.AssertDone(t)

	// Single packet split into two frames.
	SplitAndSend_aont_rs(t, w, []byte{
		// SIG frame header.
		0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 4, 0,
		// IPv4 header.
		0x40, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		51, 52, 53, 54, 55, 56,
	})
	SplitAndSend_aont_rs(t, w, []byte{
		// SIG frame header.
		0, 1, 255, 255, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 5, 0,
		// Payload.
		57, 58,
	})
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		51, 52, 53, 54, 55, 56, 57, 58,
	})
	mt.AssertDone(t)

	// Packet at a non-zero position in the frame.
	SplitAndSend_aont_rs(t, w, []byte{
		// SIG frame header.
		0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 6, 0,
		// IPv4 header.
		0x40, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload (unfinished).
		51, 52, 53, 54, 55, 56,
	})
	SplitAndSend_aont_rs(t, w, []byte{
		// SIG frame header.
		0, 1, 0, 2, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 0,
		// Payload (continued).
		57, 58,
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		61, 62, 63,
	})
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		51, 52, 53, 54, 55, 56, 57, 58,
	})
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		61, 62, 63,
	})
	mt.AssertDone(t)

	// A hole in the packet sequence.
	SplitAndSend_aont_rs(t, w, []byte{
		// SIG frame header.
		0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 8, 0,
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		101, 102, 103,
	})
	SplitAndSend_aont_rs(t, w, []byte{
		// SIG frame header.
		0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 10, 0,
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		201, 202, 203,
	})
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		101, 102, 103,
	})
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		201, 202, 203,
	})
	mt.AssertDone(t)

	// A frame with the trailing part of the packet is dropped.
	// The half-read packet should be discarded.
	// The trailing bytes at the beginning of the subsequent frame
	// should be ignored.
	SplitAndSend_aont_rs(t, w, []byte{
		// SIG frame header.
		0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 11, 0,
		// IPv4 header.
		0x40, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload (unfinished).
		51, 52, 53, 54, 55, 56,
	})
	SplitAndSend_aont_rs(t, w, []byte{
		// SIG frame header.
		0, 1, 0, 8, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 13, 0,
		// Payload (a trailing part, but not the continuation of the previous payload).
		70, 71, 72, 73, 74, 75, 76, 77,
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		201, 202, 203,
	})
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		201, 202, 203,
	})
	mt.AssertDone(t)

	// Invalid packet. The remaining part of the frame should be dropped, but
	// the processing should catch up in the next frame.
	SplitAndSend_aont_rs(t, w, []byte{
		// SIG frame header.
		0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 14, 0,
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload (unfinished).
		81, 82, 83,
		// IPv5 header - error!
		0x50, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 16, 18, 19, 20,
	})
	SplitAndSend_aont_rs(t, w, []byte{
		// SIG frame header.
		0, 1, 0, 8, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 15, 0,
		// Invalid packet (continued).
		21, 22, 23, 24, 25, 26, 27, 28,
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		91, 92, 93,
	})
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		81, 82, 83,
	})
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		91, 92, 93,
	})
	mt.AssertDone(t)

	// One packet split into 3 frames.
	SplitAndSend_aont_rs(t, w, []byte{
		// SIG frame header.
		0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 4, 0,
		// IPv4 header.
		0x40, 0, 0, 30, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		51, 52, 53, 54, 55, 56,
	})
	SplitAndSend_aont_rs(t, w, []byte{
		// SIG frame header.
		0, 1, 255, 255, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 5, 0,
		// Payload.
		57, 58,
	})
	SplitAndSend_aont_rs(t, w, []byte{
		// SIG frame header.
		0, 1, 255, 255, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 6, 0,
		// Payload.
		59, 60,
	})
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 30, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		51, 52, 53, 54, 55, 56, 57, 58, 59, 60,
	})
	mt.AssertDone(t)



	// For backup
	// Single frame with a single IPv4 packet inside.
	// SplitAndSend(t, w, []byte{
	// 	// SIG frame header.
	// 	0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 0,
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	101, 102, 103,
	// })
	// mt.AssertPacket(t, []byte{
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	101, 102, 103,
	// })
	// mt.AssertDone(t)
	//
	// // Single frame with a single IPv6 packet inside.
	// SplitAndSend(t, w, []byte{
	// 	// SIG frame header.
	// 	0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 2, 0,
	// 	// IPv6 header.
	// 	0x60, 0, 0, 0, 0, 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	101, 102, 103,
	// })
	// mt.AssertPacket(t, []byte{
	// 	// IPv6 header.
	// 	0x60, 0, 0, 0, 0, 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	101, 102, 103,
	// })
	// mt.AssertDone(t)
	//
	// // Single frame with two packets inside.
	// SplitAndSend(t, w, []byte{
	// 	// SIG frame header.
	// 	0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 3, 0,
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	101, 102, 103,
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	201, 202, 203,
	// })
	// mt.AssertPacket(t, []byte{
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	101, 102, 103,
	// })
	// mt.AssertPacket(t, []byte{
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	201, 202, 203,
	// })
	// mt.AssertDone(t)
	//
	// // Single packet split into two frames.
	// SplitAndSend(t, w, []byte{
	// 	// SIG frame header.
	// 	0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 4, 0,
	// 	// IPv4 header.
	// 	0x40, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	51, 52, 53, 54, 55, 56,
	// })
	// SplitAndSend(t, w, []byte{
	// 	// SIG frame header.
	// 	0, 1, 255, 255, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 5, 0,
	// 	// Payload.
	// 	57, 58,
	// })
	// mt.AssertPacket(t, []byte{
	// 	// IPv4 header.
	// 	0x40, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	51, 52, 53, 54, 55, 56, 57, 58,
	// })
	// mt.AssertDone(t)
	//
	// // Packet at a non-zero position in the frame.
	// SplitAndSend(t, w, []byte{
	// 	// SIG frame header.
	// 	0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 6, 0,
	// 	// IPv4 header.
	// 	0x40, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload (unfinished).
	// 	51, 52, 53, 54, 55, 56,
	// })
	// SplitAndSend(t, w, []byte{
	// 	// SIG frame header.
	// 	0, 1, 0, 2, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 0,
	// 	// Payload (continued).
	// 	57, 58,
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	61, 62, 63,
	// })
	// mt.AssertPacket(t, []byte{
	// 	// IPv4 header.
	// 	0x40, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	51, 52, 53, 54, 55, 56, 57, 58,
	// })
	// mt.AssertPacket(t, []byte{
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	61, 62, 63,
	// })
	// mt.AssertDone(t)
	//
	// // A hole in the packet sequence.
	// SplitAndSend(t, w, []byte{
	// 	// SIG frame header.
	// 	0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 8, 0,
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	101, 102, 103,
	// })
	// SplitAndSend(t, w, []byte{
	// 	// SIG frame header.
	// 	0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 10, 0,
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	201, 202, 203,
	// })
	// mt.AssertPacket(t, []byte{
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	101, 102, 103,
	// })
	// mt.AssertPacket(t, []byte{
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	201, 202, 203,
	// })
	// mt.AssertDone(t)
	//
	// // A frame with the trailing part of the packet is dropped.
	// // The half-read packet should be discarded.
	// // The trailing bytes at the beginning of the subsequent frame
	// // should be ignored.
	// SplitAndSend(t, w, []byte{
	// 	// SIG frame header.
	// 	0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 11, 0,
	// 	// IPv4 header.
	// 	0x40, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload (unfinished).
	// 	51, 52, 53, 54, 55, 56,
	// })
	// SplitAndSend(t, w, []byte{
	// 	// SIG frame header.
	// 	0, 1, 0, 8, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 13, 0,
	// 	// Payload (a trailing part, but not the continuation of the previous payload).
	// 	70, 71, 72, 73, 74, 75, 76, 77,
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	201, 202, 203,
	// })
	// mt.AssertPacket(t, []byte{
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	201, 202, 203,
	// })
	// mt.AssertDone(t)
	//
	// // Invalid packet. The remaining part of the frame should be dropped, but
	// // the processing should catch up in the next frame.
	// SplitAndSend(t, w, []byte{
	// 	// SIG frame header.
	// 	0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 14, 0,
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload (unfinished).
	// 	81, 82, 83,
	// 	// IPv5 header - error!
	// 	0x50, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 16, 18, 19, 20,
	// })
	// SplitAndSend(t, w, []byte{
	// 	// SIG frame header.
	// 	0, 1, 0, 8, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 15, 0,
	// 	// Invalid packet (continued).
	// 	21, 22, 23, 24, 25, 26, 27, 28,
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	91, 92, 93,
	// })
	// mt.AssertPacket(t, []byte{
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	81, 82, 83,
	// })
	// mt.AssertPacket(t, []byte{
	// 	// IPv4 header.
	// 	0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	91, 92, 93,
	// })
	// mt.AssertDone(t)
	//
	// // One packet split into 3 frames.
	// SplitAndSend(t, w, []byte{
	// 	// SIG frame header.
	// 	0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 4, 0,
	// 	// IPv4 header.
	// 	0x40, 0, 0, 30, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	51, 52, 53, 54, 55, 56,
	// })
	// SplitAndSend(t, w, []byte{
	// 	// SIG frame header.
	// 	0, 1, 255, 255, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 5, 0,
	// 	// Payload.
	// 	57, 58,
	// })
	// SplitAndSend(t, w, []byte{
	// 	// SIG frame header.
	// 	0, 1, 255, 255, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 6, 0,
	// 	// Payload.
	// 	59, 60,
	// })
	// mt.AssertPacket(t, []byte{
	// 	// IPv4 header.
	// 	0x40, 0, 0, 30, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// 	// Payload.
	// 	51, 52, 53, 54, 55, 56, 57, 58, 59, 60,
	// })
	// mt.AssertDone(t)
}
