// Copyright 2017 ETH Zurich
// Copyright 2019 ETH Zurich, Anapaya Systems
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
	"encoding/binary"
	"fmt"
	"container/list"

	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/ringbuf"

	"github.com/cloud9-tools/go-galoisfield"
)

const (
	// frameBufCap is the size of a preallocated frame buffer.
	frameBufCap = 65535
	// freeFramesCap is the number of preallocated Framebuf objects.
	freeFramesCap = 1024
)

var (
	// Cache of the frame buffers free to be used.
	freeFrames *ringbuf.Ring
)

func newFrameBufs(frames ringbuf.EntryList) int {
	if freeFrames == nil {
		initFreeFrames()
	}
	n, _ := freeFrames.Read(frames, true)
	return n
}

// frameBuf is a struct used to reassemble encapsulated packets spread over
// multiple SIG frames. It contains the raw bytes and metadata needed for reassembly.
type frameBuf struct {
	// Session Id of the frame.
	sessId uint8
	// Sequence number of the frame.
	seqNr uint64
	// Index of the frame.
	index int
	// Total length of the frame (including 16-byte header).
	frameLen int
	// Start of the fragment that starts a new packet. 0 means that there
	// is no such fragment. This points to the start of the header of the packet,
	// i.e., the 2-byte packet len preceding the packet header is not included.
	frag0Start int
	// Whether fragment 0 has been processed already when reassembling.
	frag0Processed bool
	// Whether fragment N has been processed already when reassembling. Fragment N
	// denotes the fragment that completes a packet. Note that with the way packets
	// are in encapsulated, such a fragment will always be at the start of a frame
	// (if there is one).
	fragNProcessed bool
	// Whether all packets completely contained in the frame have been processed.
	completePktsProcessed bool
	// The packet len of the packet that starts at fragment0. Has no meaning
	// if there is no such fragment.
	pktLen int
	// The raw bytes buffer for the frame.
	raw []byte
	// The sender object for the frame.
	snd ingressSender
}

func newFrameBuf() *frameBuf {
	buf := &frameBuf{raw: make([]byte, frameBufCap)}
	buf.Reset()
	return buf
}

// Reset resets the metadata of a FrameBuf.
func (fb *frameBuf) Reset() {
	fb.sessId = 0
	fb.seqNr = 0xffffffffffffffff
	fb.index = -1
	fb.frameLen = 0
	fb.frag0Start = 0
	fb.frag0Processed = false
	fb.fragNProcessed = false
	fb.completePktsProcessed = false
	fb.pktLen = 0
	fb.snd = nil
}

// Release reset the FrameBuf and releases it back to the ringbuf (if set).
func (fb *frameBuf) Release() {
	fb.Reset()
	if freeFrames == nil {
		initFreeFrames()
	}
	freeFrames.Write(ringbuf.EntryList{fb}, true)
}

// ProcessCompletePkts write all complete packets in the frame to the wire and
// sets the correct metadata in case there is a fragment at the end of the frame.
func (fb *frameBuf) ProcessCompletePkts(ctx context.Context) {
	logger := log.FromCtx(ctx)
	if fb.completePktsProcessed || fb.index == 0xffff {
		fb.completePktsProcessed = true
		return
	}
	offset := fb.index + sigHdrSize
	var pktLen int
	for offset < fb.frameLen {
		// Make sure that the frame contains the entire IPv4 or IPv6 header.
		// Get the payload length.
		ipVersion := fb.raw[offset] >> 4
		switch ipVersion {
		case 4:
			if fb.frameLen-offset < 20 {
				fb.completePktsProcessed = true
				return
			}
			pktLen = int(binary.BigEndian.Uint16(fb.raw[offset+2 : offset+4]))
			if pktLen < 20 {
				fb.completePktsProcessed = true
				return
			}
		case 6:
			if fb.frameLen-offset < 40 {
				fb.completePktsProcessed = true
				return
			}
			pktLen = int(binary.BigEndian.Uint16(fb.raw[offset+4 : offset+6]))
			pktLen += 40
		default:
			fb.completePktsProcessed = true
			return
		}
		rawPkt := fb.raw[offset:fb.frameLen]
		if len(rawPkt) < pktLen {
			break
		}
		// We got everything for the packet. Write it out to the wire.
		if err := fb.snd.send(rawPkt[:pktLen]); err != nil {
			logger.Error("Unable to send packet", "err", err)
		}
		offset += pktLen
	}
	if offset < fb.frameLen {
		// There is an incomplete packet at the end of the frame.
		fb.frag0Start = offset
		fb.pktLen = pktLen
	}
	fb.completePktsProcessed = true
	fb.frag0Processed = fb.frag0Start == 0
}

// Processed returns true if all fragments in the frame have been processed,
func (fb *frameBuf) Processed() bool {
	return (fb.completePktsProcessed && fb.fragNProcessed &&
		(fb.frag0Start == 0 || fb.frag0Processed))
}

// SetProcessed marks a frame as being processed.
func (fb *frameBuf) SetProcessed() {
	fb.completePktsProcessed = true
	fb.fragNProcessed = true
	fb.frag0Processed = true
}

func (fb *frameBuf) String() string {
	return fmt.Sprintf("SeqNr: %d Index: %d Len: %d frag0Start: %d processed: (%t, %t, %t)",
		fb.seqNr, fb.index, fb.frameLen, fb.frag0Start, fb.fragNProcessed, fb.frag0Processed,
		fb.completePktsProcessed)
}

func initFreeFrames() {
	freeFrames = ringbuf.New(freeFramesCap, func() interface{} {
		return newFrameBuf()
	}, "ingress_free")
}

type frameBufGroup struct {
	// The first 56 bits of SeqNr
	groupSeqNr uint64
	// The size of the group
	numPaths uint8
	// The number of frames we get
	frameCnt uint8
	// The frames with the same groupSeqNr
	frames []*frameBuf
	// The combined frame
	combined *frameBuf
	// Is the group combined
	isCombined bool
}

func NewFrameBufGroup(fb *frameBuf, numPaths uint8) *frameBufGroup {
	groupSeqNr := fb.seqNr >> 8
	pathIndex := uint8(fb.seqNr & 0xff)
	if pathIndex >= numPaths {
		// Error: path index out of bound
		return nil
	}
	fbg := &frameBufGroup{
		groupSeqNr: groupSeqNr,
		numPaths: numPaths,
		frameCnt: 1,
		frames: make([]*frameBuf, numPaths),
		combined: &frameBuf{raw: make([]byte, frameBufCap*int(numPaths))},
		isCombined: false,
	}
	fbg.frames[pathIndex] = fb
	fbg.combined.Reset()
	return fbg
}

func (fbg *frameBufGroup) Release() {
	for i := 0; i < int(fbg.numPaths); i++ {
		if fbg.frames[i] != nil {
			fbg.frames[i].Release()
		}
	}
}

func (fbg *frameBufGroup) TryAndCombine() bool {
	if fbg.isCombined {
		return true
	}
	if fbg.frameCnt != fbg.numPaths {
		return false
	}
	// combine
	frame := fbg.combined
	ref := fbg.frames[0]
	frame.index = ref.index
	frame.seqNr = ref.seqNr
	frame.snd = ref.snd
	copy(frame.raw[:hdrLen], ref.raw[:hdrLen])

	unfunishedFrames := list.New()
	for i := 0; i < int(fbg.numPaths) && fbg.frames[i].frameLen > hdrLen; i++ {
		unfunishedFrames.PushBack(i)
	}
	
	now, frameLen := hdrLen, hdrLen
	for unfunishedFrames.Len() > 0 {
		n := unfunishedFrames.Len()
		currBytes := make([]byte, 0)
		removedFrame := make([]*list.Element, 0)
		for uFrame := unfunishedFrames.Front(); uFrame != nil; uFrame = uFrame.Next() {
			frameId := uFrame.Value.(int)
			currBytes = append(currBytes, fbg.frames[frameId].raw[now])
			if fbg.frames[frameId].frameLen <= now+1 {
				removedFrame = append(removedFrame, uFrame)
			}
		}
		for i := 0; i < len(removedFrame); i++ {
			unfunishedFrames.Remove(removedFrame[i])
		}
		now++
		copy(frame.raw[frameLen : frameLen+n], AONTDecoder(currBytes))
		frameLen += n
	}

	frame.frameLen = frameLen
	return true
}

func AONTDecoder(bytes []byte) []byte {
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
