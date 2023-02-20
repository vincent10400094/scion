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

	"github.com/scionproto/scion/go/lib/serrors"
)

func PacketConn(c Conn) ExtendedPacketConn {
	return &underlayConnWrapper{c}
}

// underlayConnWrapper wraps a specialized underlay conn into a net.PacketConn
// implementation. Only *net.UDPAddr addressing is supported.
type underlayConnWrapper struct {
	// Conn is the wrapped underlay connection object.
	Conn
}

func (o *underlayConnWrapper) ReadPacket(p []byte) (int, net.Addr, net.IP, error) {
	return o.Conn.ReadPacket(p)
}

func (o *underlayConnWrapper) ReadFrom(p []byte) (int, net.Addr, error) {
	return o.Conn.ReadFrom(p)
}

func (o *underlayConnWrapper) WriteTo(p []byte, a net.Addr) (int, error) {
	udpAddr, ok := a.(*net.UDPAddr)
	if !ok {
		return 0, serrors.New("address is not UDP", "addr", a)
	}
	return o.Conn.WriteTo(p, udpAddr)
}

func (o *underlayConnWrapper) LocalAddr() net.Addr {
	return o.Conn.LocalAddr()
}
