package dataplane

import (
	"context"
	"sync"
	"time"

	"github.com/scionproto/scion/pkg/log"
)

type Decoder struct {
	// requiredSharesForDecode is equal to T in a (T,N) secret sharing scheme
	requiredSharesForDecode int
	// shareBufGroupMap is a map of shareBufGroups for each groupSeqNr
	shareBufGroupMap map[uint64]*shareBufGroup
	// mutex for the shareBufGroupMap
	mutex sync.Mutex
	// aesKey is the AES key used to decrypt the frames after combining the shares
	// The key is currently provided in the config .toml file
	aesKey string
}

func newDecoder(requiredSharesForDecode int, aesKey string) *Decoder {
	d := &Decoder{
		requiredSharesForDecode: requiredSharesForDecode,
		shareBufGroupMap:        make(map[uint64]*shareBufGroup),
		aesKey:                  aesKey,
	}
	go func() {
		defer log.HandlePanic()
		d.runCleanupLoop()
	}()
	return d
}

func (d *Decoder) Insert(ctx context.Context, share *shareBuf) *frameBuf {
	groupSeqNr := uint64(share.seqNr >> 8)

	d.mutex.Lock()
	defer func() {
		d.mutex.Unlock()
	}()
	sbg, ok := d.shareBufGroupMap[groupSeqNr] // this is executed despite cleanup having the lock

	if !ok {
		// There is no sbg for the groupSeqNr, so create one
		sbg = NewShareBufGroup(share, uint8(d.requiredSharesForDecode))
		d.shareBufGroupMap[groupSeqNr] = sbg
	}

	if ok {
		// sbg already existed

		// Check if groupSeqNr is already combined
		if sbg.isCombined {
			share.Release()
			return nil
		}

		sbg.Insert(share)
	}

	combinedFrame := sbg.TryAndCombine(ctx)
	if combinedFrame == nil {
		// Combination was unsuccessful.
		return nil
	}

	// AES-Decrypt the combined frame
	decryptedFrame, err := Decrypt(combinedFrame.raw[hdrLen:combinedFrame.frameLen], d.aesKey)
	if err != nil {
		return nil
	}
	n := copy(combinedFrame.raw[hdrLen:], decryptedFrame)
	combinedFrame.frameLen = hdrLen + n
	return combinedFrame
}

func (d *Decoder) runCleanupLoop() {
	for {
		time.Sleep(3 * time.Second)
		d.cleanup()
	}
}

// cleanup loops over all shareBufGroups and removes the ones that are marked for cleanup if a
// shareBufGroup is marked for cleanup, it will be marked for cleanup. Hence, no sbg will exist
// longer than 2 cleanup intervals.
func (d *Decoder) cleanup() {
	d.mutex.Lock()
	for groupSeqNr, sbg := range d.shareBufGroupMap {

		if sbg.isCombined {
			// sbg is already released, we only need to delete it from the map
			// fmt.Println("----Decoder cleanup: sbg is combined, deleting from map")
			delete(d.shareBufGroupMap, groupSeqNr)
			continue
		}

		if sbg.isMarkedForCleanup {
			// delete sbg
			sbg.Release()
			delete(d.shareBufGroupMap, groupSeqNr)
		} else {
			sbg.isMarkedForCleanup = true
		}
	}
	d.mutex.Unlock()
}
