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
	"fmt"
	"hash/crc64"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"

	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/metrics"
	"github.com/scionproto/scion/go/lib/snet"
)

var (
	crcTable = crc64.MakeTable(crc64.ECMA)
)

type PathStatsPublisher interface {
	PublishEgressStats(fingerprint string, frames int64, bytes int64)
}

// SessionMetrics report traffic and error counters for a session. They must be instantiated with
// the labels "remote_isd_as" and "policy_id".
type SessionMetrics struct {
	// IPPktsSent is the IP packets count sent.
	IPPktsSent metrics.Counter
	// IPPktBytesSent is the IP packet bytes sent.
	IPPktBytesSent metrics.Counter
	// FramesSent is the frames count sent.
	FramesSent metrics.Counter
	// FrameBytesSent is the frame bytes sent.
	FrameBytesSent metrics.Counter
	// SendExternalError is the error count when sending frames to the external network.
	SendExternalErrors metrics.Counter
}

type Session struct {
	SessionID          uint8
	GatewayAddr        net.UDPAddr
	DataPlaneConn      net.PacketConn
	PathStatsPublisher PathStatsPublisher
	Metrics            SessionMetrics

	mutex sync.Mutex
	// senders is a list of currently used senders.
	senders []*sender
	// number of paths being used
	numPaths uint8
	// selected senders for multipath transmission.
	selectedSenders []*sender
	// multipath encoder
	encoder *encoder
	// previously used mtu sum,
	// used to decide if the frame size of encoder should be changed.
	currentMtuSum int
	// seq is the next frame sequence number to use.
	seq uint64
}

func NewSession(sessionId uint8, gatewayAddr net.UDPAddr,
	dataPlaneConn net.PacketConn, pathStatsPublisher PathStatsPublisher,
	metrics SessionMetrics, numPaths uint8) *Session {
	sess := &Session{
		SessionID:          sessionId,
		GatewayAddr:        gatewayAddr,
		DataPlaneConn:      dataPlaneConn,
		PathStatsPublisher: pathStatsPublisher,
		Metrics:            metrics,
		numPaths:           numPaths,
		encoder:            newEncoder(sessionId, NewStreamID(), hdrLen+40),
		currentMtuSum:      hdrLen,
		seq:                0,
	}
	go func() {
		defer log.HandlePanic()
		sess.run()
	}()
	return sess
}

// Close signals that the session should close up its internal Connections. Close returns as
// soon as forwarding goroutines are signaled to shut down (never blocks).
func (s *Session) Close() {
	for _, snd := range s.senders {
		snd.Close()
	}
	s.encoder.Close()
}

// Write writes the packet to encoder's buffer asynchronously.
// The packet may be silently dropped.
func (s *Session) Write(packet gopacket.Packet) {
	if len(s.selectedSenders) == 0 {
		return
	}
	s.encoder.Write(packet.Data())
}

func (s *Session) String() string {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	res := fmt.Sprintf("ID: %d", s.SessionID)
	for _, snd := range s.senders {
		res += fmt.Sprintf("\n    %v", snd.path)
	}
	return res
}

// SetPaths sets the paths for subsequent packets encapsulated by the session.
// Packets that were written up to this point will still be sent via the old
// path. There are two reasons for that:
//
// 1. New path may have smaller MTU causing the already buffered frame not to
// fit in.
//
// 2. Paths can have different latencies, meaning that switching to new path
// could cause packets to be delivered out of order. Using new sender with new stream
// ID causes creation of new reassemby queue on the remote side, thus avoiding the
// reordering issues.
func (s *Session) SetPaths(paths []snet.Path) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	created := make([]*sender, 0, len(paths))
	reused := make(map[*sender]bool, len(s.senders))
	for _, existingSender := range s.senders {
		reused[existingSender] = false
	}

	for _, path := range paths {
		// Find out whether we already have a sender for this path.
		// Keep using old senders whenever possible.
		if existingSender, ok := findSenderWithPath(s.senders, path); ok {
			reused[existingSender] = true
			continue
		}

		newSender, err := newSender(
			s.SessionID,
			s.DataPlaneConn,
			path,
			s.GatewayAddr,
			s.PathStatsPublisher,
			s.Metrics,
		)
		if err != nil {
			// Collect newly created senders to avoid go routine leak.
			for _, createdSender := range created {
				createdSender.Close()
			}
			return err
		}
		created = append(created, newSender)
	}

	newSenders := created
	for existingSender, reuse := range reused {
		if !reuse {
			existingSender.Close()
			continue
		}
		newSenders = append(newSenders, existingSender)
	}

	// Sort the paths to get a minimal amount of consistency,
	// at least in the case when new paths are the same as old paths.
	sort.Slice(newSenders, func(x, y int) bool {
		return strings.Compare(string(newSenders[x].pathFingerprint),
			string(newSenders[y].pathFingerprint)) == -1
	})
	s.senders = newSenders
	s.selectPaths()
	return nil
}

func (s *Session) selectPaths() {
	selectedSenders := s.selectSenders()
	// Sort the selected paths by their MTU to interleave packets more conveniently
	sort.Slice(selectedSenders, func(x, y int) bool {
		return (selectedSenders[x].Mtu < selectedSenders[y].Mtu)
	})
	s.selectedSenders = selectedSenders

	// Re-compute MTU sum after selecting the senders
	mtuSum := hdrLen
	for _, s := range s.selectedSenders {
		mtuSum += (s.Mtu - hdrLen)
	}
	// Packets and packet fragments not read are dropped,
	// and the encoder is replaced by a new one with new mtu sum.
	// Session ID and stream ID are not used here.
	if s.currentMtuSum != mtuSum {
		s.encoder.ChangeFrameSize(mtuSum)
		s.currentMtuSum = mtuSum
	}
}

// select s.numPaths paths from available paths.
// TODO: implement path scheduler
func (s *Session) selectSenders() []*sender {
	// when there is no need to select senders,
	// including the case that currently there is no path.
	numPaths := int(s.numPaths)
	n := len(s.senders)
	if numPaths >= n {
		return s.senders
	}

	// Fisher–Yates shuffle
	// Not very random, but fine for now
	selectedSenders := make([]*sender, numPaths)
	array := make([]int, n)
	for i := 0; i < n; i++ {
		array[i] = i
	}
	seed := int(time.Now().UnixNano() & 0xffff)
	for i := 0; i < numPaths; i++ {
		last := n - 1 - i
		choice := seed % (last + 1)
		if last != choice {
			array[last], array[choice] = array[choice], array[last]
		}
		selectedIndex := array[last]
		selectedSenders[i] = s.senders[selectedIndex]
	}
	return selectedSenders
}

func (s *Session) run() {
	for {
		// There is a race condition issue.
		// If the paths changes after encoder.Read() returns and before the mutex is locked,
		// the value of currentMtuSum will be different to len(frame)
		frame := s.encoder.Read()
		if frame == nil {
			// Sender was closed and all the buffered frames were sent.
			break
		}

		// Lock to avoid that paths changes during sending the packets.
		s.mutex.Lock()
		// If the path changes, and mtu sum becomes smaller than the frame,
		// then silently drop the frame.
		if len(frame) > s.currentMtuSum {
			s.mutex.Unlock()
			continue
		}

		// Split and interleave the packets, then send through selected paths.
		s.splitAndSend(frame)
		s.seq++
		s.mutex.Unlock()
	}
}

// Split frame with each path's MTU .
func (s *Session) splitAndSend(frame []byte) {
	// if n < numPaths, some senders are reused to send the splits.
	n := len(s.selectedSenders)
	splits := make([][]byte, n)
	for i := 0; i < n; i++ {
		splits[i] = make([]byte, 0)
	}

	payload := frame[hdrLen:]
	splitId, minSplitId := 0, 0
	for i := 0; i < len(payload); i++ {
		if splitId >= n {
			splitId = minSplitId
		}
		for len(splits[splitId]) >= s.selectedSenders[splitId].Mtu {
			splitId++
			minSplitId = splitId
		}
		splits[splitId] = append(splits[splitId], payload[i])
		splitId++
	}

	// Send each split with respective path
	index := binary.BigEndian.Uint16(frame[indexPos : indexPos+2])
	for i := 0; i < n; i++ {
		sender := s.selectedSenders[i]
		encodedFrame := s.encodeFrame(splits[i], index, sender.StreamID, uint8(i))
		sender.Write(encodedFrame)
	}

	// Send empty frames to use all numPaths.
	numPaths := int(s.numPaths)
	for i := n; i < numPaths; i++ {
		sender := s.selectedSenders[i%n]
		encodedFrame := s.encodeFrame(make([]byte, 0), 0, 0, uint8(i))
		sender.Write(encodedFrame)
	}
}

func (s *Session) encodeFrame(pkt []byte, index uint16, streamID uint32, pathId uint8) []byte {
	frame := make([]byte, len(pkt)+hdrLen)
	copy(frame[hdrLen:], pkt)
	// Write the header.
	frame[versionPos] = 0
	frame[sessPos] = s.SessionID
	binary.BigEndian.PutUint16(frame[indexPos:indexPos+2], index)
	binary.BigEndian.PutUint32(frame[streamPos:streamPos+4], streamID&0xfffff)
	// The last 8 bits of sequence number is used as path ID.
	seq := s.seq
	binary.BigEndian.PutUint64(frame[seqPos:seqPos+8], (seq<<8)+uint64(pathId))

	return frame
}

func findSenderWithPath(senders []*sender, path snet.Path) (*sender, bool) {
	for _, s := range senders {
		if pathsEqual(path, s.path) {
			return s, true
		}
	}
	return nil, false
}

func pathsEqual(x, y snet.Path) bool {
	if x == nil && y == nil {
		return true
	}
	if x == nil || y == nil {
		return false
	}
	return snet.Fingerprint(x) == snet.Fingerprint(y) &&
		x.Metadata() != nil && y.Metadata() != nil &&
		x.Metadata().MTU == y.Metadata().MTU &&
		x.Metadata().Expiry.Equal(y.Metadata().Expiry)
}

func extractQuintuple(packet gopacket.Packet) []byte {
	// Protocol number and addresses.
	var proto layers.IPProtocol
	var q []byte
	switch ip := packet.NetworkLayer().(type) {
	case *layers.IPv4:
		q = []byte{byte(ip.Protocol)}
		q = append(q, ip.SrcIP...)
		q = append(q, ip.DstIP...)
		proto = ip.Protocol
	case *layers.IPv6:
		q = []byte{byte(ip.NextHeader)}
		q = append(q, ip.SrcIP...)
		q = append(q, ip.DstIP...)
		proto = ip.NextHeader
	default:
		panic(fmt.Sprintf("unexpected network layer %T", packet.NetworkLayer()))
	}
	// Ports.
	switch proto {
	case layers.IPProtocolTCP:
		pos := len(q)
		q = append(q, 0, 0, 0, 0)
		tcp := packet.Layer(layers.LayerTypeTCP).(*layers.TCP)
		binary.BigEndian.PutUint16(q[pos:pos+2], uint16(tcp.SrcPort))
		binary.BigEndian.PutUint16(q[pos+2:pos+4], uint16(tcp.DstPort))
	case layers.IPProtocolUDP:
		pos := len(q)
		q = append(q, 0, 0, 0, 0)
		udp := packet.Layer(layers.LayerTypeUDP).(*layers.UDP)
		binary.BigEndian.PutUint16(q[pos:pos+2], uint16(udp.SrcPort))
		binary.BigEndian.PutUint16(q[pos+2:pos+4], uint16(udp.DstPort))
	}
	return q
}
