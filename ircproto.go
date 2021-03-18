// Package ircproto provides a reconnecting IRC client library with support for
// ping handling, TLS, IRCv3 capability negotiation and SASL.
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

// Reconnecting IRC client.
type Client struct {
	cfg           Config
	mutex         sync.RWMutex
	cond          *sync.Cond
	conn          *ircconn.Conn
	notifyOpen    chan struct{}
	spawnerActive uint32
	stopping      uint32
}

// Create a new IRC client, which maintains a persistent connection to an IRC
// server and reconnects as necessary. For a more powerful interface, use
// New().
func NewSimple(ncfg *ircneg.Config, urls ...string) (*Client, error) {
	return New(Simple(ncfg, urls...))
}

// Create a new IRC client, which maintains a persistent connection to an IRC
// server and reconnects as necessary.
func New(cfg *Config) (*Client, error) {
	c := &Client{
		cfg: *cfg,
	}

	c.cond = sync.NewCond(&c.mutex)

	// Use more conservative default delays for IRC.
	if c.cfg.Backoff.InitialDelay == 0 {
		c.cfg.Backoff.InitialDelay = 10 * time.Second
	}
	if c.cfg.Backoff.MaxDelay == 0 {
		c.cfg.Backoff.MaxDelay = 10 * time.Minute
	}

	c.cfg.Backoff.MaxTries = 0
	c.startSpawner()
	return c, nil
}

func (c *Client) Close() {
	if !atomic.CompareAndSwapUint32(&c.stopping, 0, 1) {
		return
	}

	c.deadConn(nil)
}

var ErrDisconnected = fmt.Errorf("IRC client is not currently connected")

func (c *Client) WriteMsg(ctx context.Context, msg *ircparse.Msg) error {
	conn := c.CurrentConn()
	if conn == nil {
		c.startSpawner()
		return ErrDisconnected
	}

	err := conn.WriteMsg(ctx, msg)
	if err != nil {
		c.deadConn(conn)
		return err
	}

	return nil
}

func (c *Client) ReadMsg(ctx context.Context) (*ircparse.Msg, error) {
	conn := c.CurrentConn()
	if conn == nil {
		var err error
		conn, err = c.waitForConn()
		if err != nil {
			return nil, err
		}
	}

	msg, err := conn.ReadMsg(ctx)
	if err != nil {
		c.deadConn(conn)
		return nil, err
	}

	return msg, nil
}

func (c *Client) deadConn(conn *ircconn.Conn) {
	c.clearOldConn(conn)
	c.startSpawner()
}

func (c *Client) CurrentConn() *ircconn.Conn {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.conn
}

func (c *Client) snrSetNewConn(conn *ircconn.Conn) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.conn != nil {
		c.conn.Close()
	}
	c.conn = conn
	c.cond.Broadcast()
}

func (c *Client) clearOldConn(conn *ircconn.Conn) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.conn != nil && (c.conn == conn || conn == nil) {
		c.conn.Close()
		c.conn = nil
	}
}

func (c *Client) waitForConn() (*ircconn.Conn, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	for c.conn == nil {
		if atomic.LoadUint32(&c.stopping) != 0 {
			return nil, ErrDisconnected
		}
		c.cond.Wait()
	}

	return c.conn, nil
}

func (c *Client) startSpawner() {
	if atomic.LoadUint32(&c.stopping) != 0 {
		return
	}

	if !atomic.CompareAndSwapUint32(&c.spawnerActive, 0, 1) {
		return
	}

	go func() {
		defer atomic.StoreUint32(&c.spawnerActive, 0)
		c.snrLoop()
	}()
}

func (c *Client) snrLoop() {
	if conn := c.CurrentConn(); conn != nil {
		if !conn.IsDead() {
			return
		}
		c.snrSetNewConn(nil)
	}

	for {
		err := c.snrAttemptConnect()
		if err == nil {
			break
		}

		if !c.cfg.Backoff.Sleep() {
			panic("...")
		}
	}
}

func (c *Client) snrAttemptConnect() error {
	var err error
	var cfg *ircconn.Config

	if c.cfg.ConnConfigFunc != nil {
		cfg, err = c.cfg.ConnConfigFunc()
		if err != nil {
			return err
		}
	} else {
		cfg = &ircconn.Config{}
		cfg.SetDefaults()
	}

	urls, err := c.cfg.URLListFunc()
	if err != nil {
		return err
	}

	ncfg, err := c.cfg.NegConfigFunc()
	if err != nil {
		return err
	}

	conn, err := ircconn.Dial(context.Background(), cfg, urls)
	if err != nil {
		return err
	}

	_, err = ircneg.Negotiate(context.Background(), conn, ncfg)
	if err != nil {
		conn.Close()
		return err
	}

	c.cfg.Backoff.Reset()
	c.snrSetNewConn(conn)
	return nil
}
