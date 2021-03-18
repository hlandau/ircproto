// Package ircproto provides an auto-reconnecting IRC client library including
// support IRCv3 capabilities, message tags and SASL.
package ircproto

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hlandau/ircproto/ircconn"
	"github.com/hlandau/ircproto/ircneg"
	"github.com/hlandau/ircproto/ircparse"
)

const (
	stateConnecting = iota
	stateActive
	stateTerminated
)

// Reconnecting IRC client.
type Client struct {
	cfg Config

	mutex   sync.Mutex // covers state, conn, negRes, prepend, cond
	state   int        // state{Connecting,Active,Terminated}
	conn    *ircconn.Conn
	negRes  *ircneg.Result
	prepend ircconn.Source
	cond    *sync.Cond

	stopping      bool // True if stopping; do not reconnect on conn failure.
	snrRunning    uint32
	snrCtx        context.Context
	snrCancelFunc context.CancelFunc
}

// Create a new IRC client, which maintains a persistent connection to an IRC
// server and reconnects as necessary.
func New(cfg *Config) (*Client, error) {
	c := &Client{
		cfg:   *cfg,
		state: stateConnecting,
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.cond = sync.NewCond(&c.mutex)
	c.snrCtx, c.snrCancelFunc = context.WithCancel(context.Background())

	// Use more conservative default delays for IRC.
	if c.cfg.Backoff.InitialDelay == 0 {
		c.cfg.Backoff.InitialDelay = 10 * time.Second
	}
	if c.cfg.Backoff.MaxDelay == 0 {
		c.cfg.Backoff.MaxDelay = 10 * time.Minute
	}

	c.cfg.Backoff.MaxTries = 0 // always retry forever
	c.ensureSpawner()
	return c, nil
}

// Returned when calling WriteMsg if not currently connected, or when calling
// either WriteMsg or ReadMsg after the client has been closed.
var ErrDisconnected = fmt.Errorf("not currently connected to IRC")

// Tears down the current connection, if any. This is an abrupt connection
// close without sending e.g. a QUIT command; any messages which were queued
// for transmission which have yet to be processed by the server might be lost.
// For a clean shutdown, send a QUIT command and call Deprecate() instead.
func (c *Client) Close() {
	c.close(true)
}

// Indicates that a new connection should not be made once the current
// connection fails. This should be used before sending QUIT in a clean
// shutdown sequence as otherwise the client would immediately reconnect upon
// losing its connection to the server.
func (c *Client) Deprecate() {
	c.close(false)
}

func (c *Client) close(immediate bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.stopping = true
	c.snrCancelFunc()
	if immediate {
		c.notifyFailure(nil)
	}
}

// If the client currently has an active connection, return the negotiation
// result data. Otherwise, returns nil.
func (c *Client) NegotiateResult() *ircneg.Result {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.state != stateActive {
		return nil
	}

	return c.negRes
}

// Writes a message to the current connection. If there is no current
// connection, returns ErrDisconnected and the message is not queued.
//
// If an error occurs during transmission, the current connection is destroyed
// and the error which occurred is returned. Expect subsequent calls to
// WriteMsg() to return ErrDisconnected for a while.
func (c *Client) WriteMsg(ctx context.Context, msg *ircparse.Msg) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.state != stateActive {
		return ErrDisconnected
	}

	conn := c.conn
	c.mutex.Unlock()
	err := conn.WriteMsg(ctx, msg)
	c.mutex.Lock()
	if err != nil {
		c.notifyFailure(err)
		return err
	}

	return nil
}

// Reads a message from the current connection. If there is no current
// connection, blocks until a connection is available.
//
// If an error occurs during read, the current connection is destroyed and the
// error which occurred is returned. Subsequent calls to this method will then
// block until a new connection is available. In other words, errors being
// returned indicate the connection has failed and that a new connection is
// being attempted, and you can then read from the new connection (whenever it
// is successfully created, which may be some time in the future) by calling
// ReadMsg() again.
func (c *Client) ReadMsg(ctx context.Context) (*ircparse.Msg, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Wait until we are in the Active state. May fail if we have terminated.
	err := c.waitActive()
	if err != nil {
		return nil, err
	}

	prepend := c.prepend
	c.mutex.Unlock()
	msg, err := prepend.ReadMsg(ctx)
	c.mutex.Lock()
	if err != nil {
		c.notifyFailure(err)
		return nil, err
	}

	return msg, nil
}

// Current connection has failed, so move to Connecting state and get a new one.
// Must hold lock.
func (c *Client) notifyFailure(err error) {
	if c.state != stateActive {
		return
	}

	c.conn.Close()
	c.conn = nil
	c.negRes = nil
	c.prepend = nil

	if c.stopping {
		c.state = stateTerminated
	} else {
		c.state = stateConnecting
		c.ensureSpawner()
	}
}

// Wait until state is Active (returns nil) or Terminated (returns non-nil).
// Must hold lock.
func (c *Client) waitActive() error {
	for c.state == stateConnecting {
		c.cond.Wait()
	}

	if c.state == stateTerminated {
		return ErrDisconnected
	}

	return nil
}

// Ensure the spawner exists and is trying to connect.
// Must hold lock. c.state must be stateConnecting.
func (c *Client) ensureSpawner() {
	if !atomic.CompareAndSwapUint32(&c.snrRunning, 0, 1) {
		return
	}

	go c.snrLoop()
}

// Keep trying to connect in a loop until we succeed or are terminated.
func (c *Client) snrLoop() {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	defer atomic.StoreUint32(&c.snrRunning, 0)

	for {
		if c.state != stateConnecting {
			panic("not in connecting state")
		}

		c.mutex.Unlock()
		conn, res, err := c.snrAttemptConnect(c.snrCtx)
		c.mutex.Lock()
		if err == nil {
			c.snrEmitNewConn(conn, res)
			break
		}

		if c.snrTerminate() {
			break
		}

		c.mutex.Unlock()
		if !c.cfg.Backoff.Sleep() {
			panic("backoff should not fail")
		}
		c.mutex.Lock()
	}
}

// Does not touch the state, so the lock need not and should not be held.
func (c *Client) snrAttemptConnect(ctx context.Context) (*ircconn.Conn, *ircneg.Result, error) {
	var err error
	var cfg *ircconn.Config

	if c.cfg.ConnConfigFunc != nil {
		cfg, err = c.cfg.ConnConfigFunc()
		if err != nil {
			return nil, nil, err
		}
	} else {
		cfg = &ircconn.Config{}
		cfg.SetDefaults()
	}

	urls, err := c.cfg.URLListFunc()
	if err != nil {
		return nil, nil, err
	}

	ncfg, err := c.cfg.NegConfigFunc()
	if err != nil {
		return nil, nil, err
	}

	conn, err := ircconn.Dial(ctx, cfg, urls)
	if err != nil {
		return nil, nil, err
	}

	res, err := ircneg.Negotiate(ctx, conn, ncfg)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}

	// Negotiation was successful so reset the backoff.
	c.cfg.Backoff.Reset()
	return conn, res, nil
}

// New successful connection, make it available.
// Must hold lock.
func (c *Client) snrEmitNewConn(conn *ircconn.Conn, res *ircneg.Result) {
	if c.state != stateConnecting || c.conn != nil {
		panic("should not be an existing conn")
	}

	// Late termination just after we succeeded?
	if c.snrTerminate() {
		conn.Close()
		return
	}

	c.state = stateActive
	c.conn = conn
	c.negRes = res
	c.prepend = ircconn.PrependMsgs(conn, res.ReadMsgs)
	c.cond.Broadcast()
}

// Check if termination is due. Returns true if terminated.
// Must hold lock.
func (c *Client) snrTerminate() bool {
	if !c.stopping {
		return false
	}

	c.state = stateTerminated
	c.cond.Broadcast()
	return true
}
