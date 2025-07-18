package dataplane

import (
	"container/list"
	"context"

	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/private/ringbuf"
)

type shareBufGroup struct {
	// groupSeqNr is the first 56 bits of SeqNr of every share in the group
	groupSeqNr uint64
	// numPaths is T in a (T, N) secret sharing scheme
	numPaths uint8
	// The shares with the same groupSeqNr
	shares *list.List
	// Is the group combined
	isCombined bool
	// Is the group marked for cleanup in the decoder
	isMarkedForCleanup bool
}

func GetPathIndex(sb *shareBuf) uint8 {
	return uint8(sb.seqNr & 0xff)
}

func NewShareBufGroup(sb *shareBuf, numPaths uint8) *shareBufGroup {
	groupSeqNr := sb.seqNr >> 8
	pathIndex := GetPathIndex(sb)
	if pathIndex >= 255 {
		// Error: path index out of bounds
		return nil
	}
	sbg := &shareBufGroup{
		groupSeqNr:         groupSeqNr,
		numPaths:           numPaths,
		shares:             list.New(),
		isCombined:         false,
		isMarkedForCleanup: false,
	}
	sbg.Insert(sb)
	return sbg
}

func (sbg *shareBufGroup) Release() {
	for e := sbg.shares.Front(); e != nil; e = e.Next() {
		e.Value.(*shareBuf).Release()
	}
}

func (sbg *shareBufGroup) Insert(sb *shareBuf) {
	sbg.shares.PushBack(sb)
}

// TryAndCombine tries to combine the shares. If this group has numPaths many shares, the combined
// frame is returned, otherwise it returns nil.
func (sbg *shareBufGroup) TryAndCombine(ctx context.Context) *frameBuf {
	logger := log.FromCtx(ctx)

	if uint8(sbg.shares.Len()) < sbg.numPaths {
		// Not enough shares for combination
		return nil
	}

	firstFrame := sbg.shares.Front().Value.(*shareBuf)

	// Extract shares from the group into a slice
	shares := make([][]byte, sbg.numPaths)
	for i, e := 0, sbg.shares.Front(); e != nil && i < int(sbg.numPaths); i, e = i+1, e.Next() {
		sb := e.Value.(*shareBuf)
		shares[i] = sb.raw[hdrLen:sb.frameLen]
	}

	// Combine the shares
	output, err := Combine(shares)
	if err != nil {
		logger.Debug("Error combining shares.", "err", err)
		return nil
	}

	// Create a new frameBuf and copy the header and the combined share into it

	readEntries := make(ringbuf.EntryList, 1)
	n := newFrameBufs(readEntries)

	if n != 1 {
		return nil
	}

	combinedFrame := readEntries[0].(*frameBuf)
	copy(combinedFrame.raw[:hdrLen], firstFrame.raw[:hdrLen])
	copy(combinedFrame.raw[hdrLen:], []byte(output))

	combinedFrame.seqNr = firstFrame.seqNr >> 8
	combinedFrame.frameLen = len(output) + hdrLen
	combinedFrame.fragNProcessed = combinedFrame.index == 0
	combinedFrame.completePktsProcessed = combinedFrame.index == 0xffff

	sbg.isCombined = true
	sbg.Release()

	return combinedFrame
}
