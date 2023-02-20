// Copyright 2022 ETH Zurich
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

//go:build go1.9 && linux
// +build go1.9,linux

// Package conn implements underlay sockets.
package conn

import (
	"net"
	"syscall"
)

// ExtendedPacketConn extends the regular net.PacketConn with a method that allows the caller
// to obtain the destination address of the packet.
// This method is useful for e.g. the dispatcher who needs to know the destination of the
// packets when it listens on e.g. 0.0.0.0, to redirect to the correct colibri service if C=1.
type ExtendedPacketConn interface {
	net.PacketConn
	// ReadPacket behaves identically as net.PacketConn.ReadFrom but it additionally returns
	// the destination address present in the IP packet.
	ReadPacket(buff []byte) (n int, from net.Addr, to net.IP, err error)
}

// ipProtoDetails is used in Conn to abstract the details of how these functions work in the
// different IPv4 and IPv6 implementations.
type ipProtoDetails interface {
	SetSocketOptions(*net.UDPConn) error
	BuildAddress(syscall.Sockaddr) *net.UDPAddr
	GetDstAddress([]syscall.SocketControlMessage) (net.IP, error)
}
