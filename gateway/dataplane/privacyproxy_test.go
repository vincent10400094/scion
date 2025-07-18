package dataplane

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/stretchr/testify/assert"

	"github.com/scionproto/scion/pkg/private/mocks/net/mock_net"
	"github.com/scionproto/scion/pkg/private/xtest"
	"github.com/scionproto/scion/pkg/snet"
)

func TestEncryptionAndDecryption(t *testing.T) {
	message := []byte("Hello World!")
	encryptedMessage, err := Encrypt(message, testAESKey)
	assert.Nil(t, err)

	decryptedMessage, err := Decrypt(encryptedMessage, testAESKey)
	assert.Nil(t, err)

	assert.Equal(t, message, decryptedMessage)
}

func TestThreePathsEncryptionWithRandomData(t *testing.T) {
	fmt.Println("[Running Test]: privacyproxy_test.go->TestThreePathsEncryptionWithRandomData")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	numPackets := 4
	random := rand.New(rand.NewSource(42))

	// Unbuffered channel guarantees that the frames won't be sent out
	// immediately, but only when waitFrames is called.
	frameChan := make(chan ([]byte))

	sess := createMockSession(ctrl, frameChan)

	addr := &snet.UDPAddr{
		IA: xtest.MustParseIA("1-ff00:0:300"),
		Host: &net.UDPAddr{
			IP:   net.IP{192, 168, 1, 1},
			Port: 80,
		},
	}

	mt := &MockTun{}
	w := newWorker(addr, 1, 2, mt, IngressMetrics{}, testAESKey)

	// create a list of randomly generated gopackets and send them
	packets := make([]gopacket.Packet, numPackets)
	for i := 0; i < numPackets; i++ {
		packets[i] = generateRandomPayloadPacket(random, i)
		sess.Write(packets[i])
	}
	waitFramesProxyTest(t, frameChan, w)

	assert.Equal(t, numPackets, len(mt.packets))
	for i := 0; i < numPackets; i++ {
		assert.Equal(t, packets[i].Data(), mt.packets[i])
	}

	sess.Close()
}

func TestChangingPaths(t *testing.T) {
	fmt.Println("[Running Test]: privacyproxy_test.go->TestChangingPaths")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	numPackets := 2
	random := rand.New(rand.NewSource(42))

	// Unbuffered channel guarantees that the frames won't be sent out
	// immediately, but only when waitFrames is called.
	frameChan := make(chan ([]byte))

	sess := createMockSession(ctrl, frameChan)

	addr := &snet.UDPAddr{
		IA: xtest.MustParseIA("1-ff00:0:300"),
		Host: &net.UDPAddr{
			IP:   net.IP{192, 168, 1, 1},
			Port: 80,
		},
	}

	mt := &MockTun{}
	w := newWorker(addr, 1, 2, mt, IngressMetrics{}, testAESKey)

	// create a list of randomly generated gopackets and send them
	packets := make([]gopacket.Packet, 2*numPackets)
	for i := 0; i < numPackets; i++ {
		packets[i] = generateRandomPayloadPacket(random, 400)
		sess.Write(packets[i])
	}

	waitFramesProxyTest(t, frameChan, w)
	sess.SetPaths([]snet.Path{
		createMockPath(ctrl, 500),
		createMockPath(ctrl, 10001),
		createMockPath(ctrl, 8002),
	})
	for i := 0; i < numPackets; i++ {
		packets[i+numPackets] = generateRandomPayloadPacket(random, 200)
		sess.Write(packets[i])
	}
	waitFramesProxyTest(t, frameChan, w)
	assert.Equal(t, numPackets*2, len(mt.packets))
	for i := 0; i < numPackets; i++ {
		assert.Equal(t, packets[i].Data(), mt.packets[i])
	}

	sess.Close()
}

// func TestReordering(t *testing.T) {
// 	fmt.Println("[Running Test]: privacyproxy_test.go->TestThreePathsEncryptionWithRandomData")
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	random := rand.New(rand.NewSource(42))

// 	// Unbuffered channel guarantees that the frames won't be sent out
// 	// immediately, but only when waitFrames is called.
// 	frameChan := make(chan ([]byte))

// 	sess := createMockSession(ctrl, frameChan)

// 	addr := &snet.UDPAddr{
// 		IA: xtest.MustParseIA("1-ff00:0:300"),
// 		Host: &net.UDPAddr{
// 			IP:   net.IP{192, 168, 1, 1},
// 			Port: 80,
// 		},
// 	}

// 	mt := &MockTun{}
// 	w := newWorker(addr, 1, 2, mt, IngressMetrics{})

// 	// create a list of randomly generated gopackets and send them
// 	packets := make([]gopacket.Packet, 2)
// 	for i := 0; i < 2; i++ {
// 		packets[i] = generateRandomPayloadPacket(random, i)
// 		sess.Write(packets[i])
// 	}

// 	frameBuffer := make([][]byte, 10)
// 	frameBufferIndex := 0

// Top:
// 	for {
// 		select {
// 		case frame := <-frameChan:
// 			frameBuffer[frameBufferIndex] = frame
// 			frameBufferIndex++
// 		case <-time.After(1500 * time.Millisecond):
// 			fmt.Println("----[Debug]: 1500ms timout while waiting for frames from network")
// 			break Top
// 		}
// 	}

// 	SendFrame(t, w, frameBuffer[1])
// 	SendFrame(t, w, frameBuffer[0])

// 	assert.Equal(t, 2, len(mt.packets))
// 	for i := 0; i < 2; i++ {
// 		assert.Equal(t, packets[i].Data(), mt.packets[i])
// 	}

// 	sess.Close()
// }

func createMockSession(ctrl *gomock.Controller, frameChan chan []byte) *Session {
	conn := mock_net.NewMockPacketConn(ctrl)
	conn.EXPECT().LocalAddr().Return(&net.UDPAddr{IP: net.IP{192, 168, 1, 1}}).AnyTimes()
	conn.EXPECT().WriteTo(gomock.Any(), gomock.Any()).DoAndReturn(
		func(f []byte, _ interface{}) (int, error) {
			frameChan <- f
			return 0, nil
		}).AnyTimes()

	sess := NewSession(22, net.UDPAddr{}, conn, nil, SessionMetrics{}, 2, 3, testAESKey)

	sess.SetPaths([]snet.Path{
		createMockPath(ctrl, 300),
		createMockPath(ctrl, 301),
		createMockPath(ctrl, 302),
	})
	return sess
}

// Creates a a packet with a fixed IPv4 header and a random payload of random length.
func generateRandomPayloadPacket(r *rand.Rand, payloadSize int) gopacket.Packet {
	// payloadSizeRandom := r.Int()%(maxPayloadSize-32) + 32
	bytes := append([]byte{
		// IPv4 header.
		0x40, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	}, make([]byte, payloadSize)...)

	binary.BigEndian.PutUint16(bytes[2:4], uint16(20+payloadSize))

	// set payload content of bytes to random generated bytes
	r.Read(bytes[20:])
	for i := 0; i < payloadSize; i++ {
		bytes[i+20] = byte(i%64 + payloadSize%10)
	}

	decodeOptions := gopacket.DecodeOptions{
		NoCopy: true,
		Lazy:   true,
	}
	pkt := gopacket.NewPacket(bytes, layers.LayerTypeIPv4, decodeOptions)
	return pkt
}

func waitFramesProxyTest(t *testing.T, frameChan chan []byte, e *worker) {
Top:
	for {
		select {
		case frame := <-frameChan:
			SendFrame(t, e, frame)
		case <-time.After(1500 * time.Millisecond):
			fmt.Println("----[Debug]: 1500ms timout while waiting for frames from network")
			break Top
		}
	}
}
