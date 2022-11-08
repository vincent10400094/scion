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

package coliquic

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"

	"github.com/lucas-clemente/quic-go"
	"github.com/scionproto/scion/go/lib/log"
)

// Listener is a net.Listener backed by a quic listener.
// It will permanently listen for sessions, and once a session is opened, it will keep
// listening for streams in that session. This allows clients, e.g. PersistentQUIC, to just
// spawn a new stream if they already had a session with the server.
type Listener struct {
	pconn      net.PacketConn
	tlsConfig  *tls.Config
	quicConfig *quic.Config

	listener    quic.Listener
	listenerMux sync.Mutex
	newConns    chan *streamAsConn
	acceptErrs  chan error
}

func NewListener(pconn net.PacketConn, tlsConfig *tls.Config, quicConfig *quic.Config) *Listener {
	return &Listener{
		pconn:      pconn,
		tlsConfig:  tlsConfig,
		quicConfig: quicConfig,
		newConns:   make(chan *streamAsConn),
		acceptErrs: make(chan error),
	}
}

// Accept waits for a new session or a new stream to be established and creates a connection
// out of it.
// Accept is typically called in a Loop.
func (l *Listener) Accept() (net.Conn, error) {
	// create a listener only once. Cannot use sync.Once as we want to return immediately if
	// quic.Listen returned an error, and at the same time in this case, would want
	// to cancel the sync.Once.
	l.listenerMux.Lock()
	if l.listener == nil {
		var err error
		l.listener, err = quic.Listen(l.pconn, l.tlsConfig, l.quicConfig)
		if err != nil {
			return nil, err
		}
		go func() {
			defer log.HandlePanic()
			l.acceptNewSessions()
		}()
	}
	l.listenerMux.Unlock()
	// we have a listener. The listener is always listening for new sessions,
	// and when a new session is established, it will wait for new streams
	var conn net.Conn
	var err error
	select {
	case conn = <-l.newConns:
	case err = <-l.acceptErrs:
	}
	return conn, err
}

func (l *Listener) Close() error {
	l.listenerMux.Lock()
	defer l.listenerMux.Unlock()
	if l.listener == nil {
		return nil
	}
	return l.listener.Close()
}

func (l *Listener) Addr() net.Addr {
	l.listenerMux.Lock()
	defer l.listenerMux.Unlock()
	if l.listener == nil {
		return nil
	}
	return l.listener.Addr()
}

func (l *Listener) acceptNewSessions() {
	for {
		sess, err := l.listener.Accept(context.Background())
		if err != nil {
			log.Debug("error listening for new session", "err", err)
			if netErr, ok := err.(net.Error); ok {
				if netErr.Temporary() || netErr.Timeout() {
					continue // don't give up
				}
			}
			l.acceptErrs <- err
			return // the error is not recoverable
		}
		go func() {
			defer log.HandlePanic()
			l.acceptNewStreams(sess)
			err = sess.CloseWithError(0, "")
			if err != nil {
				log.Info("session was closed with an error", "err", err)
			}
		}()
	}
}

func (l *Listener) acceptNewStreams(sess quic.Session) {
	for {
		stream, err := sess.AcceptStream(context.Background())
		if err != nil {
			var netErr net.Error
			// timeout errors are very common: if the other end times out or this end does,
			// the connection is closed.
			// Log other times of error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				log.Debug("error listening for new streams", "err", err)
			}
			// exit the function, regardless of the error
			return
		}
		conn := &streamAsConn{
			stream:  stream,
			session: sess,
		}
		l.newConns <- conn
	}
}
