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
	"sync/atomic"

	"github.com/hlandau/ircproto/ircparse"
)

type MsgConn interface {
}

// Conn represents an IRC client protocol connection. It represents, and has
// the lifetime of, a single transport protocol connection; there is no
// automatic reconnection functionality. It is primarily intended for use as
// part of a higher-level protocol library which implements automatic
// reconnection via a succession of Conns, but can be used directly if desired.
type Conn struct {
	conn     net.Conn // transport connection (TCP, TLS, etc.)
	rdr      *bufio.Reader
	cfg      Config
	teardown uint32
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
func (conn *Conn) WriteRaw(raw string) error {
	if conn.IsDead() {
		return ErrClosed
	}

	_, err := conn.conn.Write([]byte(raw))
	if err != nil {
		return err
	}

	return nil
}

// Serialize a message and write it to the server.
func (conn *Conn) WriteMsg(msg *ircparse.Msg) error {
	raw, err := msg.String()
	if err != nil {
		return err
	}

	return conn.WriteRaw(raw)
}

func (conn *Conn) rxMsgActual(needParsed bool) (raw string, msg *ircparse.Msg, err error) {
	raw, err = conn.rdr.ReadString('\n')
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

func (conn *Conn) rxMsg() (raw string, msg *ircparse.Msg, err error) {
	raw, msg, err = conn.rxMsgActual(!conn.cfg.InhibitPingHandling)
	if err != nil {
		return
	}

	if !conn.cfg.InhibitPingHandling && msg.Command == "PING" {
		err = conn.WriteMsg(&ircparse.Msg{Command: "PONG", Args: msg.Args})
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
func (conn *Conn) ReadRaw() (string, error) {
	raw, _, err := conn.rxMsg()
	return raw, err
}

// Read a message from the server and deserialize it.
//
// It is essential that the consumer of this library call this method or
// ReadRaw in a loop forever until the connection is torn down (e.g. until this
// method returns an error), as internal ping handling is based on the
// assumption that this (or ReadRaw) will be called frequently.
func (conn *Conn) ReadMsg() (*ircparse.Msg, error) {
	_, msg, err := conn.rxMsg()
	return msg, err
}

// Read a message from the server and return it in both raw and parsed forms.
func (conn *Conn) ReadMsgBoth() (string, *ircparse.Msg, error) {
	return conn.rxMsg()
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

//IRCNick      string // IRC nickname.
//IRCUser      string // IRC username.
//IRCReal      string // IRC realname.
//IRCPass      string // IRC server password. This is not an account password.
//SASLUsername string // IRCv3 SASL username. Leave blank to disable SASL.
//SASLPassword string // IRCv3 SASL password.

// Do not use StartTLS over cleartext connections if it is available.
// Irrelevant if InheritCapSupport is set, as StartTLS requires cap support.
//InhibitStartTLS bool

// Do not attempt to use IRCv3 capabilities negotiation with the server.
// SASLUsername and SASLPassword will be ignored if set.
//InhibitCapSupport bool

// Set if you want to do your own handshaking. Not recommended/advanced use
// only. Implies InhibitCapSupport.
//InhibitHandshake bool

// For ircs:// connections which use TLS from the outset, TLSDialer.Config
// (or TLSDialContext) is used and determines the TLS configuration used. The
// below configuration is used for TLS connections established using
// STARTTLS.
//StartTLSConfig *tls.Config

// ...

/*
	if conn.cfg.InhibitHandshake {
		return nil
	}

		if !conn.cfg.InhibitCapSupport {
			conn.WriteMsg(&ircparse.Msg{
				Command: "CAP LS",
				Args:    []string{"302"},
			})
		}

		err := conn.WriteMsg(&ircparse.Msg{
			Command: "USER",
			Args:    []string{conn.cfg.IRCUser, "*", "*", conn.cfg.IRCReal},
		})
		if err != nil {
			return err
		}

		err = conn.WriteMsg(&ircparse.Msg{
			Command: "NICK",
			Args:    []string{conn.cfg.IRCNick},
		}) // NICK
		if err != nil {
			return err
		}
*/
