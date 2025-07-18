// Copyright 2020 Anapaya Systems
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
	"encoding/binary"
	"math"
	"time"
)

// Each SIG frame starts with SIG frame header with the following format:
//
//  0                   1                   2                   3
//  0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//  |     Version   |    Session    |            Index              |
//  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//  |   Reserved (12 bits)    |          Stream (20 bits)           |
//  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//  |                                                               |
//  +                       Sequence number                         +
//  |                                                               |
//  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//
// The header is followed by raw IP packets (or parts thereof) one directly
// following another with no intermediate padding.

const (
	// Length of the frame header, in bytes.
	hdrLen = 16
	// Location of individual fields in the frame header.
	versionPos = 0
	sessPos    = 1
	indexPos   = 2
	streamPos  = 4
	seqPos     = 8
)

// encoder reads packets from a ring buffer and transforms them into SIG frames.
type encoder struct {
	// sessionID of the session this encoder belongs to.
	sessionID uint8
	// streamID identifies a flow within the session. Only the frames from
	// the same streams are, on the remote side, put into the same reassembly queue.
	streamID uint32
	// ring is used to pass packets from the writer goroutine to the sending goroutine.
	ring *pktRing
	// seq is the next frame sequence number to use.
	seq uint64
	// pkt is the unprocessed part of the currently processed packet.
	pkt []byte
	// frame is the frame being built at the moment.
	// To avoid allocations, we reuse the same frame buffer over and over again.
	frame []byte
	// maxMessageLength is the maximum number of bytes that can be read from packets such that the
	// resulting encrypted frame is still below the MTU
	maxMessageLength int
	// aesKey is the key used to encrypt the frame. The key is currently provided in the config
	// .toml file
	aesKey string
}

// newEncoder creates a new encoder instance.
// mtu is max size of the frame, excluding SCION header, but including SIG header.
func newEncoder(sessionID uint8, streamID uint32, aesKey string) *encoder {
	return &encoder{
		sessionID: sessionID,
		streamID:  streamID,
		seq:       0,
		ring:      newPktRing(),
		frame:     make([]byte, 0),
		aesKey:    aesKey,
	}
}

// Close initiates the close procedure. Frames can still be read.
// Once there are no more frames available, Read will return nil.
func (e *encoder) Close() {
	e.ring.Close()
}

// Write sends a packet to the encoder.
func (e *encoder) Write(pkt []byte) {
	e.ring.Write(pkt, false)
}

// calculateMaxMessageLengthForMTU subtracts the padding from the MTU that is added by AES
func calculateMaxMessageLengthForMTU(mtu int) int {
	if mtu < 40 {
		return 0
	}
	return 3*(int(math.Floor(float64(mtu-40)/4.0))) + 2
}

// ReadEncryptedSIGFrame reads a SIG frame using ReadRegularSIGFrame and encrypts it.
func (e *encoder) ReadEncryptedSIGFrame(mtu int) []byte {

	e.maxMessageLength = calculateMaxMessageLengthForMTU(mtu - 1) // -1 because the secret sharing scheme takes up one tag byte for reconstruction

	e.frame = make([]byte, 0, mtu-1)
	e.frame = e.frame[:hdrLen]
	// Write the header.
	e.frame[versionPos] = 0
	e.frame[sessPos] = e.sessionID
	binary.BigEndian.PutUint16(e.frame[indexPos:indexPos+2], 0xffff)
	binary.BigEndian.PutUint32(e.frame[streamPos:streamPos+4], e.streamID&0xfffff)
	binary.BigEndian.PutUint64(e.frame[seqPos:seqPos+8], e.seq)

	// Increase the sequence number of the share group by 256 as they are identified by the last
	// byte
	e.seq += 256
	frame := e.ReadRegularSIGFrame()

	if frame == nil {
		return nil
	}

	// encrypt the frame
	share, err := Encrypt(frame[hdrLen:], e.aesKey)
	if err != nil {
		panic(err)
	}

	frame = frame[:hdrLen+len(share)]
	copy(frame[hdrLen:], share)
	return frame
}

// Reads a SIG frame from the encoder.
// The function blocks if there are no frames available.
// When the encoder is closed, the function returns nil.
// Fills up e.frame with e.maxMessageLength bytes
func (e *encoder) ReadRegularSIGFrame() []byte {

	// First, use the data remaining from the last packet, if any.
	var pos int = hdrLen
	if len(e.pkt) > 0 {
		pos += e.copyToFrame()
		if len(e.pkt) > 0 {
			return e.frame[:pos]
		}
	}

	// Read more packets and fill in as much of the frame as possible.
	var indexSet bool
	for {
		// Check whether one more packet would fit into the frame.
		// At least 40B are needed to fit IPv6 header into it.
		if e.maxMessageLength-pos < 40 {
			return e.frame[:pos]
		}

		// If there's nothing but the header in the current frame we are going to fetch more
		// data in blocking manner. If there's already some data in the frame we will
		// still try to stuff it with more packets, but if there are no packets available,
		// we'll send what we have immediately.
		block := (pos == hdrLen)
		var n int
		e.pkt, n = e.ring.Read(block)
		if n == 0 {
			// No more packets to stuff into the frame. Go on with sending.
			return e.frame[:pos]
		}
		if n == -1 {
			if block {
				// Ringbuffer was closed.
				return nil
			} else {
				// Ringbuffer was closed, but there's still some data to send.
				// Return the frame. Next time this function will be called
				// it will return nil.
				return e.frame[:pos]
			}
		}
		// We've got a packet to stuff into the frame.
		// Let's make sure that it is a valid IPv4 or IPv6 packet.
		if len(e.pkt) == 0 {
			continue
		}
		ipVersion := e.pkt[0] >> 4
		switch ipVersion {
		case 4:
			if len(e.pkt) < 20 {
				continue
			}
			length := int(binary.BigEndian.Uint16(e.pkt[2:4]))
			if length != len(e.pkt) {
				continue
			}
		case 6:
			if len(e.pkt) < 40 {
				continue
			}
			length := 40 + int(binary.BigEndian.Uint16(e.pkt[4:6]))
			if length != len(e.pkt) {
				continue
			}
		default:
			continue
		}
		// Set the first packet index in the frame header if appropriate.
		if !indexSet {
			binary.BigEndian.PutUint16(e.frame[indexPos:indexPos+2], uint16(pos-hdrLen))
			indexSet = true
		}
		// Write the packet to the frame.
		pos += e.copyToFrame()
		if len(e.pkt) > 0 {
			// The packet doesn't fully fit into the frame. Send what we have.
			return e.frame[:pos]
		}
	}
}

// copyToFrame copies as much data as possible from the currently processed packet
// to the current frame. Returns number of bytes copied.
func (e *encoder) copyToFrame() int {
	toCopy := e.maxMessageLength - len(e.frame)
	if len(e.pkt) < toCopy {
		toCopy = len(e.pkt)
	}
	pos := len(e.frame)
	e.frame = e.frame[:pos+toCopy]
	copy(e.frame[pos:pos+toCopy], e.pkt[:toCopy])
	e.pkt = e.pkt[toCopy:]
	return toCopy
}

// NewStreamID generates a new random stream ID.
func NewStreamID() uint32 {
	return uint32(time.Now().UnixNano() & 0xffff)
}
