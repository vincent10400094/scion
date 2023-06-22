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

	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/ringbuf"
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

	splitId, minSplitId, frameLen := 0, 0, hdrLen

	// n := int(fbg.numPaths)
	n := 0
	for n < int(fbg.numPaths) && fbg.frames[n].frameLen > hdrLen {
		n++
	}
	frameIndices := make([]int, n)
	for i := 0; i < n; i++ {
		frameIndices[i] = hdrLen
	}

	// Require that the length of frames are non-decreasing
	// Should be changed with the split algorithm in session.go
	for {
		if splitId >= n {
			splitId = minSplitId
		}
		for splitId < n && frameIndices[splitId] >= fbg.frames[splitId].frameLen {
			splitId++
			minSplitId = splitId
		}
		if minSplitId >= n {
			break
		}
		frame.raw[frameLen] = fbg.frames[splitId].raw[frameIndices[splitId]]
		frameIndices[splitId]++
		splitId++
		frameLen++
	}
	frame.frameLen = frameLen
	return true
}
