package dataplane

import "github.com/scionproto/scion/private/ringbuf"

const (
	// shareBufCap is the size of a preallocated frame buffer.
	shareBufCap = 65535
	// freeFramesCap is the number of preallocated Framebuf objects.
	freeSharesCap = 1024
)

var (
	// Cache of the frame buffers free to be used.
	freeShares *ringbuf.Ring
)

func newShareBufs(encryptedframes ringbuf.EntryList) int {
	if freeShares == nil {
		initFreeShares()
	}
	n, _ := freeShares.Read(encryptedframes, true)
	return n
}

func initFreeShares() {
	freeShares = ringbuf.New(freeSharesCap, func() interface{} {
		return newShareBuf()
	}, "ingress_freeEncrypted")
}

// shareBuf is a struct used to reassemble shares into a SIG frame. shareBufs
// are managed by the Decoder. Once enough shares are received, the Decoder
// reassembles the shares into a SIG frame and AES-decodes it.
type shareBuf struct {
	// Session Id of the frame.
	sessId uint8
	// Sequence number of the frame.
	seqNr uint64
	// Total length of the frame (including 16-byte header).
	frameLen int
	// The raw bytes buffer for the frame.
	raw []byte
	// The sender object for the frame.
	snd ingressSender
}

func newShareBuf() *shareBuf {
	buf := &shareBuf{raw: make([]byte, shareBufCap)}
	buf.Reset()
	return buf
}

func (sb *shareBuf) Reset() {
	sb.sessId = 0
	sb.seqNr = 0
	sb.frameLen = 0
	sb.snd = nil
}

func (sb *shareBuf) Release() {
	sb.Reset()
	if freeShares == nil {
		initFreeShares()
	}
	freeShares.Write(ringbuf.EntryList{sb}, true)
}
