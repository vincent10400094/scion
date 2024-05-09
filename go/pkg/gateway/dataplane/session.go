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
	metrics SessionMetrics) *Session {
	sess := &Session{
		SessionID:          sessionId,
		GatewayAddr:        gatewayAddr,
		DataPlaneConn:      dataPlaneConn,
		PathStatsPublisher: pathStatsPublisher,
		Metrics:            metrics,
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
	if len(s.senders) == 0 {
		return
	}
	fmt.Printf("packet size %v\n", packet.Data())
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
	s.encoder.Lock()
	defer s.mutex.Unlock()
	defer s.encoder.Unlock()

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

	// Re-compute MTU sum after selecting the senders
	minMtu := 65536
	for _, sender := range s.senders {
		if sender.Mtu < minMtu {
			minMtu = sender.Mtu
		}
	}
	mtuSum := (minMtu - hdrLen) * len(s.senders) + hdrLen
	s.currentMtuSum = mtuSum
	s.encoder.ChangeFrameSize(mtuSum)

	return nil
}

func (s *Session) run() {
	for {
		frame := s.encoder.Read()
		if frame == nil {
			// Sender was closed and all the buffered frames were sent.
			break
		}

		// Split and interleave the packets, then send through selected paths.
		// s.splitAndSend(frame)
		s.splitAndSend_aont_rs(frame)
		s.seq++
	}
}

// AONT-RS version of splitAndSend
func (s *Session) splitAndSend_aont_rs(frame []byte) {
	// fmt.Printf("Using SplitAndSend_aont_rs\n")
	n := len(s.senders)
	if n>=3 {
		fmt.Printf("Path num bigger than or equal to 3\n")

		// Usint AONT_RS
		// The AONT_RS transfert a frame two 3 shards, the reciever need 2 out
		// of 3 for recovering.
		fmt.Printf("Splitandsend_aont_rs Frame size %v\n", len(frame))
		fmt.Printf("Splitandsend_aont_rs Frame frame %v\n", frame)
		splits := AONT_RS_Encode(frame, 2, 1)
		index := binary.BigEndian.Uint16(frame[indexPos : indexPos+2])
		streamID := binary.BigEndian.Uint32(frame[streamPos : streamPos+4])

		for i:=0; i<3; i++{
			sender := s.senders[i]
			encodedFrame := s.encodeFrame(splits[i], index, streamID, uint8(i))
			sender.Write(encodedFrame)
			fmt.Printf("encodedFrame size %v\n", len(encodedFrame))
		}

	}else{
		// Using AONT
		// fmt.Printf("Path num less than 3\n")

		dataLen := (len(frame)+n-1-hdrLen)/n
		splits := make([][]byte, n)
		for i := 0; i < n; i++ {
			splits[i] = make([]byte, dataLen)
		}
		now := hdrLen
		for i := 0; i < dataLen - 1; i++ {
			currBytes := AONTEncode(frame[now:now+n])
			for j := 0; j < n; j++ {
				splits[j][i] = currBytes[j]
			}
			now += n
		}
		// The left bytes, maybe less than n bytes
		// just pad 0
		currBytes := make([]byte, n)
		copy(currBytes, frame[now:])
		currBytes = AONTEncode(currBytes)
		for i := 0; i < n; i++ {
			splits[i][dataLen-1] = currBytes[i]
		}

		// Send each split with respective path
		index := binary.BigEndian.Uint16(frame[indexPos : indexPos+2])
		streamID := binary.BigEndian.Uint32(frame[streamPos : streamPos+4])
		for i := 0; i < n; i++ {
			sender := s.senders[i]
			encodedFrame := s.encodeFrame(splits[i], index, streamID, uint8(i))
			sender.Write(encodedFrame)
		}
	}
}


// Split frame with each path's MTU .
func (s *Session) splitAndSend(frame []byte) {
	n := len(s.senders)
	dataLen := (len(frame)+n-1-hdrLen)/n
	splits := make([][]byte, n)
	for i := 0; i < n; i++ {
		splits[i] = make([]byte, dataLen)
	}
	now := hdrLen
	for i := 0; i < dataLen - 1; i++ {
		currBytes := AONTEncode(frame[now:now+n])
		for j := 0; j < n; j++ {
			splits[j][i] = currBytes[j]
		}
		now += n
	}
	// The left bytes, maybe less than n bytes
	// just pad 0
	currBytes := make([]byte, n)
	copy(currBytes, frame[now:])
	currBytes = AONTEncode(currBytes)
	for i := 0; i < n; i++ {
		splits[i][dataLen-1] = currBytes[i]
	}

	// Send each split with respective path
	index := binary.BigEndian.Uint16(frame[indexPos : indexPos+2])
	streamID := binary.BigEndian.Uint32(frame[streamPos : streamPos+4])
	for i := 0; i < n; i++ {
		sender := s.senders[i]
		encodedFrame := s.encodeFrame(splits[i], index, streamID, uint8(i))
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
