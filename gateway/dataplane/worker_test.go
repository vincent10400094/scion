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
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/scionproto/scion/pkg/private/xtest"
	"github.com/scionproto/scion/pkg/snet"
	"github.com/scionproto/scion/private/ringbuf"
)

type MockTun struct {
	packets [][]byte
}

func (mt *MockTun) Read(p []byte) (n int, err error) {
	return n, nil
}

func (mt *MockTun) Write(p []byte) (n int, err error) {
	// copy over the data as the rlist reuses the buffer
	pCopy := make([]byte, len(p))
	copy(pCopy, p)
	mt.packets = append(mt.packets, pCopy)
	return n, nil
}

func (mt *MockTun) Close() error {
	return nil
}

func (mt *MockTun) AssertPacket(t *testing.T, expected []byte) {
	assert.NotEqual(t, 0, len(mt.packets))
	if len(mt.packets) != 0 {
		mt.packets = mt.packets[1:]
	}
}

func (mt *MockTun) AssertDone(t *testing.T) {
	assert.Equal(t, 0, len(mt.packets))
}

func SendFrame(t *testing.T, w *worker, data []byte) {
	frames := make(ringbuf.EntryList, 1)
	n := newShareBufs(frames)
	assert.Equal(t, 1, n)
	f := frames[0].(*shareBuf)
	copy(f.raw, data)
	f.frameLen = len(data)
	w.processFrame(context.Background(), f)
}

func EncryptAndSendFrame(t *testing.T, w *worker, packet []byte, seqNumber int) {
	N := 3
	T := 2
	encrypted, err := Encrypt(packet, testAESKey)
	assert.NoError(t, err)
	shares, _ := Split(encrypted, N, T)

	for i := 0; i < N; i++ {
		sigHeader := []byte{0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, byte(seqNumber), byte(i)}
		SendFrame(t, w, append(sigHeader, shares[i]...))
	}
}
func EncryptAndSendFrameWithHeader(t *testing.T, w *worker, packet []byte, sigHeader []byte, seqNumber int) {
	N := 3
	T := 2

	encrypted, err := Encrypt(packet, testAESKey)
	assert.NoError(t, err)
	shares, _ := Split(encrypted, N, T)

	for i := 0; i < N; i++ {
		sigHeader[14] = byte(seqNumber)
		sigHeader[15] = byte(i)
		SendFrame(t, w, append(sigHeader, shares[i]...))
	}
}

// Test the worker by sending mock SIG frames and checking if the output on the wire is correct.
func TestParsing(t *testing.T) {
	fmt.Println("[Running Test]: worker_test.go->TestParsing")
	addr := &snet.UDPAddr{
		IA: xtest.MustParseIA("1-ff00:0:300"),
		Host: &net.UDPAddr{
			IP:   net.IP{192, 168, 1, 1},
			Port: 80,
		},
	}
	mt := &MockTun{}
	w := newWorker(addr, 1, 2, mt, IngressMetrics{}, testAESKey)

	simpleIp4Packet := []byte{0x40, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 17, 18, 19, 20, 21, 22, 23, 24}

	EncryptAndSendFrame(t, w, simpleIp4Packet, 0)
	mt.AssertPacket(t, simpleIp4Packet)
	mt.AssertDone(t)

	// Single frame with a single IPv6 packet inside.
	simpleIp6Packet := []byte{
		// IPv6 header.
		0x60, 0, 0, 0, 0, 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		101, 102, 103}

	EncryptAndSendFrame(t, w, simpleIp6Packet, 1)
	mt.AssertPacket(t, simpleIp6Packet)
	mt.AssertDone(t)

	// Single frame with two packets inside.
	twoPacketsPacket := []byte{
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		101, 102, 103,
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		201, 202, 203,
	}
	EncryptAndSendFrame(t, w, twoPacketsPacket, 2)
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
	onePacketTwoFrames1 := []byte{
		// IPv4 header.
		0x40, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		51, 52, 53, 54, 55, 56,
	}
	onePacketTwoFrames2 := []byte{
		// Payload.
		57, 58,
	}
	EncryptAndSendFrame(t, w, onePacketTwoFrames1, 3)
	EncryptAndSendFrame(t, w, onePacketTwoFrames2, 4)
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		51, 52, 53, 54, 55, 56, 57, 58,
	})
	mt.AssertDone(t)

	// Packet at a non-zero position in the frame.
	nonZeroPosPacket1 := []byte{
		// IPv4 header.
		0x40, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload (unfinished).
		11, 12, 13, 14, 15, 16,
	}
	nonZeroPosHeader1 := []byte{0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 5}
	nonZeroPosPacket2 := []byte{
		// Payload (continued).
		17, 18,
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		21, 22, 23,
	}
	nonZeroPosHeader2 := []byte{0, 1, 0, 2, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 6}
	EncryptAndSendFrameWithHeader(t, w, nonZeroPosPacket1, nonZeroPosHeader1, 5)
	EncryptAndSendFrameWithHeader(t, w, nonZeroPosPacket2, nonZeroPosHeader2, 6)
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		11, 12, 13, 14, 15, 16, 17, 18,
	})
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		21, 22, 23,
	})
	mt.AssertDone(t)

	// A hole in the packet sequence.
	holeSequencePacket1 := []byte{
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		101, 102, 103,
	}
	holeSequenceHeader1 := []byte{0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 7}
	holeSequencePacket2 := []byte{
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		201, 202, 203,
	}
	holeSequenceHeader2 := []byte{0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 9}
	EncryptAndSendFrameWithHeader(t, w, holeSequencePacket1, holeSequenceHeader1, 7)
	EncryptAndSendFrameWithHeader(t, w, holeSequencePacket2, holeSequenceHeader2, 9)
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
	trailingDroppedPacket1 := []byte{
		// IPv4 header.
		0x40, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload (unfinished).
		51, 52, 53, 54, 55, 56,
	}
	trailingDroppedHeader1 := []byte{0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 10, 0}
	trailingDroppedPacket2 := []byte{
		// Payload (a trailing part, but not the continuation of the previous payload).
		70, 71, 72, 73, 74, 75, 76, 77,
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		201, 202, 203,
	}
	trailingDroppedHeader2 := []byte{0, 1, 0, 8, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 12, 0}
	EncryptAndSendFrameWithHeader(t, w, trailingDroppedPacket1, trailingDroppedHeader1, 10)
	EncryptAndSendFrameWithHeader(t, w, trailingDroppedPacket2, trailingDroppedHeader2, 12)
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		201, 202, 203,
	})
	mt.AssertDone(t)

	// Invalid packet. The remaining part of the frame should be dropped, but
	// the processing should catch up in the next frame.
	invalidPacket1 := []byte{
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload (unfinished).
		81, 82, 83,
		// IPv5 header - error!
		0x50, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 16, 18, 19, 20,
	}
	invalidPacketHeader1 := []byte{0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 13, 0}
	invalidPacket2 := []byte{
		// Invalid packet (continued).
		21, 22, 23, 24, 25, 26, 27, 28,
		// IPv4 header.
		0x40, 0, 0, 23, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		91, 92, 93,
	}
	invalidPacketHeader2 := []byte{0, 1, 0, 8, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 14, 0}
	EncryptAndSendFrameWithHeader(t, w, invalidPacket1, invalidPacketHeader1, 13)
	EncryptAndSendFrameWithHeader(t, w, invalidPacket2, invalidPacketHeader2, 14)
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
	packet3framesPacket1 := []byte{
		// IPv4 header.
		0x40, 0, 0, 30, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		51, 52, 53, 54, 55, 56,
	}
	packet3framesHeader1 := []byte{0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 15}
	packet3framesPacket2 := []byte{
		57, 58,
	}
	packet3framesHeader2 := []byte{0, 1, 255, 255, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 16}
	packet3framesPacket3 := []byte{
		// Payload.
		59, 60,
	}
	packet3framesHeader3 := []byte{0, 1, 255, 255, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 17}
	EncryptAndSendFrameWithHeader(t, w, packet3framesPacket1, packet3framesHeader1, 15)
	EncryptAndSendFrameWithHeader(t, w, packet3framesPacket2, packet3framesHeader2, 16)
	EncryptAndSendFrameWithHeader(t, w, packet3framesPacket3, packet3framesHeader3, 17)
	mt.AssertPacket(t, []byte{
		// IPv4 header.
		0x40, 0, 0, 30, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		// Payload.
		51, 52, 53, 54, 55, 56, 57, 58, 59, 60,
	})

	mt.AssertDone(t)
}
