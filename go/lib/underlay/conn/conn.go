// Copyright 2017 ETH Zurich
// Copyright 2020 ETH Zurich, Anapaya Systems
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
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/ipv4"

	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/sockctrl"
)

// Messages is a list of ipX.Messages. It is necessary to hide the type alias
// between ipv4.Message, ipv6.Message and socket.Message.
type Messages []ipv4.Message

// Conn describes the API for an underlay socket
type Conn interface {
	ReadFrom([]byte) (int, *net.UDPAddr, error)
	ReadPacket([]byte) (int, *net.UDPAddr, net.IP, error)
	ReadBatch(Messages) (int, error)
	Write([]byte) (int, error)
	WriteTo([]byte, *net.UDPAddr) (int, error)
	WriteBatch(Messages, int) (int, error)
	LocalAddr() *net.UDPAddr
	RemoteAddr() *net.UDPAddr
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
	SetDeadline(time.Time) error
	Close() error
}

// Config customizes the behavior of an underlay socket.
type Config struct {
	// ReceiveBufferSize is the size of the operating system receive buffer, in bytes.
	ReceiveBufferSize int
}

// New opens a new underlay socket on the specified addresses.
//
// The config can be used to customize socket behavior.
func New(listen, remote *net.UDPAddr, cfg *Config) (Conn, error) {
	a := listen
	if remote != nil {
		a = remote
	}
	if listen == nil && remote == nil {
		panic("either listen or remote must be set")
	}
	if a.IP.To4() != nil {
		return newConnUDPIPv4(listen, remote, cfg)
	}
	return newConnUDPIPv6(listen, remote, cfg)
}

type connUDPBase struct {
	conn           *net.UDPConn
	Listen         *net.UDPAddr
	Remote         *net.UDPAddr
	closed         bool
	readMu         sync.Mutex
	ipProtoHandler ipProtoDetails
}

func (cc *connUDPBase) initConnUDP(network string, laddr, raddr *net.UDPAddr, cfg *Config) error {
	var c *net.UDPConn
	var err error
	if laddr == nil {
		return serrors.New("listen address must be specified")
	}
	if raddr == nil {
		if c, err = net.ListenUDP(network, laddr); err != nil {
			return serrors.WrapStr("Error listening on socket", err,
				"network", network, "listen", laddr)
		}
	} else {
		if c, err = net.DialUDP(network, laddr, raddr); err != nil {
			return serrors.WrapStr("Error setting up connection", err,
				"network", network, "listen", laddr, "remote", raddr)
		}
	}
	// Set and confirm receive buffer size
	before, err := sockctrl.GetsockoptInt(c, syscall.SOL_SOCKET, syscall.SO_RCVBUF)
	if err != nil {
		return serrors.WrapStr("Error getting SO_RCVBUF socket option (before)", err,
			"listen", laddr, "remote", raddr)
	}
	target := cfg.ReceiveBufferSize
	if err = c.SetReadBuffer(target); err != nil {
		return serrors.WrapStr("Error setting recv buffer size", err,
			"listen", laddr, "remote", raddr)
	}
	after, err := sockctrl.GetsockoptInt(c, syscall.SOL_SOCKET, syscall.SO_RCVBUF)
	if err != nil {
		return serrors.WrapStr("Error getting SO_RCVBUF socket option (after)", err,
			"listen", laddr, "remote", raddr)
	}
	if after/2 < target {
		// Note: kernel doubles value passed in SetReadBuffer, value returned is the doubled value
		log.Info("Receive buffer size smaller than requested",
			"expected", target, "actual", after/2, "before", before/2)
	}

	// request the inclusion of the destination IP in the oob data when reading:
	if err = cc.ipProtoHandler.SetSocketOptions(c); err != nil {
		return serrors.WrapStr("error setting PKTINFO", err,
			"listen", laddr, "remote", raddr)
	}

	cc.conn = c
	cc.Listen = laddr
	cc.Remote = raddr
	return nil
}

func (c *connUDPBase) ReadPacket(buff []byte) (n int, from *net.UDPAddr, to net.IP, err error) {
	raw, err := c.conn.SyscallConn()
	if err != nil {
		return
	}

	// capture n and sa in the read method
	var sa syscall.Sockaddr
	var oobN int
	var oob [1024]byte
	err = raw.Read(func(fd uintptr) (done bool) {
		c.readMu.Lock()
		defer c.readMu.Unlock()
		n, oobN, _, sa, err = syscall.Recvmsg(int(fd), buff, oob[:], 0)
		return err != syscall.EAGAIN // return false means go again
	})
	if err != nil {
		return
	}

	from = c.ipProtoHandler.BuildAddress(sa)
	// now get the destination address from the ancillary data:
	msgs, err := syscall.ParseSocketControlMessage(oob[:oobN])
	if err != nil {
		return 0, nil, nil, err
	}
	to, err = c.ipProtoHandler.GetDstAddress(msgs)
	return
}

func (c *connUDPBase) ReadFrom(b []byte) (int, *net.UDPAddr, error) {
	return c.conn.ReadFromUDP(b)
}

func (c *connUDPBase) Write(b []byte) (int, error) {
	return c.conn.Write(b)
}

func (c *connUDPBase) WriteTo(b []byte, dst *net.UDPAddr) (int, error) {
	if c.Remote != nil {
		return c.conn.Write(b)
	}
	return c.conn.WriteTo(b, dst)
}

func (c *connUDPBase) LocalAddr() *net.UDPAddr {
	return c.Listen
}

func (c *connUDPBase) RemoteAddr() *net.UDPAddr {
	return c.Remote
}

func (c *connUDPBase) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

// NewReadMessages allocates memory for reading IPv4 Linux network stack
// messages.
func NewReadMessages(n int) Messages {
	m := make(Messages, n)
	for i := range m {
		// Allocate a single-element, to avoid allocations when setting the buffer.
		m[i].Buffers = make([][]byte, 1)
	}
	return m
}
