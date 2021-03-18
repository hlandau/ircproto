// Package ircconn provides an IRC client protocol connection with the lifetime
// of a single transport-layer connection.
package ircconn

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	neturl "net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hlandau/ircproto/ircparse"
)

// Abstract interface for an IRC message source/sink.
type Abstract interface {
	Close()
	WriteMsg(ctx context.Context, msg *ircparse.Msg) error
	ReadMsg(ctx context.Context) (*ircparse.Msg, error)
}

// Conn represents an IRC client protocol connection. It represents, and has
// the lifetime of, a single transport protocol connection; there is no
// automatic reconnection functionality. It is primarily intended for use as
// part of a higher-level protocol library which implements automatic
// reconnection via a succession of Conns, but can be used directly if desired.
//
// The I/O methods of an established Conn take a context.Context. This context
// is supported for the purposes of deadlines. A cancelled context will not be
// allowed to initiate new I/O operations, but will not cancel operations in
// progress.
type Conn struct {
	conn net.Conn // transport connection (TCP, TLS, etc.)
	rdr  *bufio.Reader
	cfg  Config

	readMutex, writeMutex sync.Mutex
	teardown              uint32
}

// Configuration for a Conn.
//
// To use client certificates, set a custom TLSDialer.Config.
type Config struct {
	// This client will automatically respond to PING commands by default. Set
	// this if you want to handle them yourself.
	InhibitPingHandling bool

	// If set, this is the function to use to attempt to initiate a TCP
	// connection. Otherwise, TCPDialer.DialContext is used.
	TCPDialContext func(ctx context.Context, net, addr string) (net.Conn, error)
	TCPDialer      net.Dialer

	// If set, this is the function to use to attempt to initiate a TLS
	// connection. Otherwise, TLSDialer.DialContext is used.
	TLSDialContext func(ctx context.Context, net, addr string) (net.Conn, error)
	TLSDialer      tls.Dialer
}

// Set reasonable defaults for the Config structure. This configures TLS
// configurations to avoid validating server certificates as the average IRC
// server's certificate configuration leaves much to be desired in terms of
// validatability.
func (cfg *Config) SetDefaults() {
	if cfg.TLSDialer.Config == nil {
		cfg.TLSDialer.Config = &tls.Config{
			InsecureSkipVerify: true,
		}
	}
}

// Establishes a connection with one of the IRC servers given in the list of
// URLs, and creates and returns the newly established Conn. This performs the
// TCP/TLS dialling for you, and can walk through the list of URLs until it
// is able to successfully connect to a server.
//
// The provided context is used during the establishment of the connection, TLS
// negotiation if applicable, and transmission of the initial handshake and may
// be used for cancellation at any time during this entire process. Once a
// connection is successfully returned, the context is no longer used; if it is
// desired to tear down the Conn after this function returns, use Close().
//
// The URLs must be in one of the following forms and are tried in order:
//
//   irc://HOSTNAME:PORT
//   irc://HOSTNAME         (default port 6667)
//   ircs://HOSTNAME:PORT
//   ircs://HOSTNAME        (default port 6697)
//
// Trailing paths, querystrings, etc. are ignored.
func Dial(ctx context.Context, cfg *Config, urls []string) (*Conn, error) {
	var lastErr error

	for _, url := range urls {
		u, err := neturl.Parse(url)
		if err != nil {
			return nil, err
		}

		if u.Scheme != "irc" && u.Scheme != "ircs" {
			return nil, fmt.Errorf("unrecognised scheme in URL, must be 'irc' or 'ircs': %q: %q", u.Scheme, url)
		}

		conn, err := dialUsingHost(ctx, cfg, u.Host, u.Scheme == "ircs")
		if err == nil {
			return conn, nil
		}

		lastErr = err
	}

	// If all URLs failed, return the connection error from the last URL tried
	return nil, lastErr
}

func dialUsingHost(ctx context.Context, cfg *Config, addr string, useTLS bool) (*Conn, error) {
	var conn net.Conn
	var err error

	_, _, err = net.SplitHostPort(addr)
	if err != nil {
		defaultPort := "6667"
		if useTLS {
			defaultPort = "6697"
		}
		addr = net.JoinHostPort(addr, defaultPort)
	}

	if useTLS {
		if cfg.TLSDialContext != nil {
			conn, err = cfg.TLSDialContext(ctx, "tcp", addr)
		} else {
			conn, err = cfg.TLSDialer.DialContext(ctx, "tcp", addr)
		}
	} else {
		if cfg.TCPDialContext != nil {
			conn, err = cfg.TCPDialContext(ctx, "tcp", addr)
		} else {
			conn, err = cfg.TCPDialer.DialContext(ctx, "tcp", addr)
		}
	}

	if err != nil {
		return nil, err
	}

	return NewConn(ctx, conn, cfg)
}

// Creates a new Conn representing an IRC protocol connection from an existing
// transport-layer connection. The connection provided should be a
// freshly-opened TCP or TLS connection which has yet to have anything
// transmitted or received on it, though other transports could also presumably
// be used.
//
// The initial IRC handshake will be transmitted immediately; if it were to
// block, this function would block. However, this function will return before
// waiting for the server to respond; use Rx methods to receive events relating
// to the connection process.
//
// The provided context is used during the transmission of the initial
// handshake and may be used for cancellation at any time during the process.
// Once a connection is successfully returned, the context is no longer used;
// if it is desired to tear down the Conn after this function returns, use
// Close().
func NewConn(ctx context.Context, transportConn net.Conn, cfg *Config) (*Conn, error) {
	conn := &Conn{
		conn: transportConn,
		rdr:  bufio.NewReader(transportConn),
		cfg:  *cfg,
	}

	return conn, nil
}

// Returned by write functions if a Conn is dead.
var ErrClosed = fmt.Errorf("closed IRC connection")

// Write a raw command to the server. This should be a string ending in "\n"
// and must be a well-formatted IRC command.
//
// If writing commands fails, there may still be buffered commands to be
// received, so it is recommended to handle this case by calling ReadMsg
// repeatedly until it returns an error. Alternatively, simply call Close();
// unlike the read methods, the write methods do not do this automatically
// when they fail.
func (conn *Conn) WriteRaw(ctx context.Context, raw string) error {
	if conn.IsDead() {
		return ErrClosed
	}

	conn.writeMutex.Lock()
	defer conn.writeMutex.Unlock()

	err := conn.updateWriteDeadline(ctx)
	if err != nil {
		return err
	}

	_, err = conn.conn.Write([]byte(raw))
	if err != nil {
		return err
	}

	return nil
}

// Serialize a message and write it to the server.
func (conn *Conn) WriteMsg(ctx context.Context, msg *ircparse.Msg) error {
	raw, err := msg.String()
	if err != nil {
		return err
	}

	return conn.WriteRaw(ctx, raw)
}

func (conn *Conn) rxMsgActualInner(ctx context.Context) (raw string, err error) {
	conn.readMutex.Lock()
	defer conn.readMutex.Unlock()

	err = conn.updateReadDeadline(ctx)
	if err != nil {
		return
	}

	return conn.rdr.ReadString('\n')
}

func (conn *Conn) rxMsgActual(ctx context.Context, needParsed bool) (raw string, msg *ircparse.Msg, err error) {
	raw, err = conn.rxMsgActualInner(ctx)
	if err != nil {
		conn.Close()
		return
	}

	if needParsed {
		msg, err = ircparse.Parse(raw)
		if err != nil {
			conn.Close()
			return
		}
	}

	return
}

func (conn *Conn) rxMsg(ctx context.Context, needParsed bool) (raw string, msg *ircparse.Msg, err error) {
	raw, msg, err = conn.rxMsgActual(ctx, needParsed || !conn.cfg.InhibitPingHandling)
	if err != nil {
		return
	}

	if !conn.cfg.InhibitPingHandling && msg.Command == "PING" {
		err = conn.WriteMsg(ctx, &ircparse.Msg{Command: "PONG", Args: msg.Args})
		if err != nil {
			return
		}
	}

	return
}

// Read a raw message from the server.
//
// It is essential that the consumer of this library call this method or
// ReadMsg in a loop forever until the connection is torn down (e.g. until this
// method returns an error), as internal ping handling is based on the
// assumption that this (or ReadMsg) will be called frequently.
func (conn *Conn) ReadRaw(ctx context.Context) (string, error) {
	raw, _, err := conn.rxMsg(ctx, false)
	return raw, err
}

// Read a message from the server and deserialize it.
//
// It is essential that the consumer of this library call this method or
// ReadRaw in a loop forever until the connection is torn down (e.g. until this
// method returns an error), as internal ping handling is based on the
// assumption that this (or ReadRaw) will be called frequently.
//
// The raw message which was parsed is available in msg.Raw.
func (conn *Conn) ReadMsg(ctx context.Context) (*ircparse.Msg, error) {
	_, msg, err := conn.rxMsg(ctx, true)
	return msg, err
}

// Teardown the connection. This does not send QUIT but simply closes the
// connection. Any data queued for transmission may be lost and not received by
// the server; if you want a clean shutdown, send a QUIT command instead.
//
// If this method has already been called, or the connection has already been
// torn down for other reasons (e.g., a Read command has returned an error),
// calling this again is harmless. This is known as the torndown state, and
// this connection is not GC-collectible until this state is reached.
func (conn *Conn) Close() {
	if !atomic.CompareAndSwapUint32(&conn.teardown, 0, 1) {
		return
	}

	conn.conn.Close()
}

// Returns true iff the Conn is in the torndown state, for example because
// Close() was called or because a Read or Write function returned an error.
func (conn *Conn) IsDead() bool {
	return atomic.LoadUint32(&conn.teardown) != 0
}

// Access the underlying net.Conn. This should only be used for doing interface
// upgrades and then querying e.g. addresses or TLS information, never for the
// Read/Write/Close methods.
func (conn *Conn) Underlying() net.Conn {
	return conn.conn
}

func (conn *Conn) updateReadDeadline(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	t, ok := ctx.Deadline()
	if ok {
		conn.conn.SetReadDeadline(t)
	} else {
		conn.conn.SetReadDeadline(time.Time{})
	}

	return nil
}

func (conn *Conn) updateWriteDeadline(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	t, ok := ctx.Deadline()
	if ok {
		conn.conn.SetWriteDeadline(t)
	} else {
		conn.conn.SetWriteDeadline(time.Time{})
	}

	return nil
}
