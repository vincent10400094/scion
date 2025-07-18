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
	"fmt"
	"hash/crc64"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/gopacket"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/pkg/metrics"
	"github.com/scionproto/scion/pkg/slayers"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
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
	mutex              sync.Mutex
	// senders is a list of currently used senders.
	senders []*sender
	// encoder is the encoder that transforms IP packets into SIG frames
	encoder *encoder
	// mtu is the minimal MTU of all paths
	mtu            int
	numberOfPathsT int
	numberOfPathsN int
}

func NewSession(sessionId uint8, gatewayAddr net.UDPAddr,
	dataPlaneConn net.PacketConn, pathStatsPublisher PathStatsPublisher,
	metrics SessionMetrics, numberOfPathsT int, numberOfPathsN int, aesKey string) *Session {
	sess := &Session{
		SessionID:          sessionId,
		GatewayAddr:        gatewayAddr,
		DataPlaneConn:      dataPlaneConn,
		PathStatsPublisher: pathStatsPublisher,
		Metrics:            metrics,
		numberOfPathsT:     numberOfPathsT,
		numberOfPathsN:     numberOfPathsN,
		encoder:            newEncoder(sessionId, NewStreamID(), aesKey),
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
	s.mutex.Lock()
	for _, snd := range s.senders {
		snd.Close()
	}
	s.encoder.Close()
	s.mutex.Unlock()
}

// Write encodes the packet and sends it to the network.
// The packet may be silently dropped.
func (s *Session) Write(packet gopacket.Packet) {
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

	// Re-compute MTU after selecting the paths
	lowestMtu := 65535
	for _, path := range paths {

		// MTU must account for the size of the SCION header.
		localAddr := s.DataPlaneConn.LocalAddr().(*snet.UDPAddr)
		addrLen := addr.IABytes*2 + len(localAddr.Host.IP) + len(s.GatewayAddr.IP)
		scionPath, _ := path.Dataplane().(snetpath.SCION)
		pathLen := len(scionPath.Raw)

		pathMtu := int(path.Metadata().MTU) - slayers.CmnHdrLen - addrLen - pathLen - udpHdrLen
		if pathMtu < lowestMtu {
			lowestMtu = pathMtu
		}
	}

	if lowestMtu != s.mtu {
		s.mtu = lowestMtu
	}

	return nil
}

func (s *Session) run() {

	fmt.Println("Session is running. T=", s.numberOfPathsT, "N=", s.numberOfPathsN)

	for {

		startTime := time.Now()
		for len(s.senders) < s.numberOfPathsN || s.mtu == 0 {
			if time.Since(startTime) > time.Second*5 {
				panic("There less than T paths available after 5 second wait.")
			}
		}

		// Get the SIG frame, then apply SSS to the content.
		sigFrame := s.encoder.ReadEncryptedSIGFrame(s.mtu)
		if sigFrame == nil {
			// sender was closed and all the buffered frames were sent.
			break
		}

		err := s.splitAndSend(sigFrame, s.numberOfPathsN, s.numberOfPathsT)
		if err != nil {
			panic(err)
		}

	}
}

func (s *Session) splitAndSend(frame []byte, N, T int) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if T > N || N > 255 || T < 1 || N < 1 || len(s.senders) < N {
		panic("Invalid N or T")
	}

	// split the frame into N shares
	shares, err := Split(frame[hdrLen:], N, T)
	if err != nil {
		return err
	}

	// create N encrypted frames
	encryptedFrames := make([][]byte, N)
	for i := 0; i < N; i++ {
		encryptedFrames[i] = make([]byte, hdrLen+len(shares[i]))
		// copy over the header from the unencrypted frame
		copy(encryptedFrames[i], frame[:hdrLen])
		// update the last byte of the sequence number to be the path ID
		encryptedFrames[i][seqPos+7] = byte(i)
		// copy over the share
		copy(encryptedFrames[i][hdrLen:], shares[i])
	}

	// write the encrypted frames to the respective senders
	for pathID, sender := range s.senders {
		sender.Write(encryptedFrames[pathID])
	}

	return nil
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
