package ircconn

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"github.com/hlandau/ircproto/ircparse"
	"io"
	"net"
	"reflect"
	"testing"
	"time"
)

type item struct {
	Msg *ircparse.Msg
}

var items = []item{
	{Msg: &ircparse.Msg{Command: "FOOBAR", Args: []string{"foo", "bar", "baz bah abae"}}},
	{Msg: &ircparse.Msg{Command: "FOOBAR", Args: []string{"foo", "bar", "baz"}}},
	{Msg: &ircparse.Msg{Command: "FOOBAR", Args: []string{"foo", "bar"}}},
	{Msg: &ircparse.Msg{Command: "FOOBAR", Args: []string{"foo"}}},
	{Msg: &ircparse.Msg{Command: "FOOBAR", Args: nil}},
	{Msg: &ircparse.Msg{ServerName: "the/quick/brown/Fox.goes.under", Command: "FOOBAR", Args: nil}},
	{Msg: &ircparse.Msg{ServerName: "2001::dead:beEF", Command: "FOOBAR", Args: nil}},
	{Msg: &ircparse.Msg{ServerName: "192.0.2.1", Command: "FOOBAR", Args: nil}},
	{Msg: &ircparse.Msg{NickName: "a|b|c", UserName: "~abc", HostName: "unknown/abc", Command: "FOOBAR", Args: nil}},
	{Msg: &ircparse.Msg{NickName: "a|b|c", UserName: "~abc", HostName: "unknown/abc", Command: "FOOBAR", Args: []string{"alpha", "beta", "gamma delta"},
		Tags: map[string]string{"foo": "bar"}}},
	{Msg: &ircparse.Msg{NickName: "a|b|c", UserName: "~abc", HostName: "unknown/abc", Command: "FOOBAR", Args: []string{"alpha", "beta", "gamma delta"},
		Tags: map[string]string{"foo": "bar", "alpha": "beta"}}},
}

func TestConn(t *testing.T) {
	clientSide, serverSide := net.Pipe()

	count := 1000
	serverCanExitChan := make(chan struct{})

	// "server" side
	go func() {
		serverConn, err := NewConn(context.Background(), serverSide, &Config{})
		if err != nil {
			panic(err)
		}
		defer serverConn.Close()
		for i := range items {
			if items[i].Msg.Tags == nil {
				items[i].Msg.Raw, _ = items[i].Msg.String()
			}
		}

		for i := 0; i < count; i++ {
			for j := range items {
				err := serverConn.WriteMsg(context.Background(), items[j].Msg)
				if err != nil {
					t.Errorf("unexpected error sending message to client: %v", err)
				}
			}
		}
		<-serverCanExitChan
	}()

	// client side
	clientConn, err := NewConn(context.Background(), clientSide, &Config{})
	if err != nil {
		panic(err)
	}
	defer clientConn.Close()
	for i := 0; i < count; i++ {
		for j := range items {
			msg, err := clientConn.ReadMsg(context.Background())
			if err != nil {
				t.Errorf("unexpected error receiving message from server: %v", err)
			}
			if items[j].Msg.Raw == "" {
				msg.Raw = ""
			}
			if !reflect.DeepEqual(msg, items[j].Msg) {
				t.Errorf("(%d) messages do not match:\n  got:      %#v\n  expected: %#v", j, msg, items[j].Msg)
			}
		}
	}

	// We have received all the messages that are going to be sent, so now check
	// that we can deadline correctly.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(1*time.Second))
	_, err = clientConn.ReadMsg(ctx)
	if err == nil {
		t.Errorf("expected error but successfully received message")
	} else if err == context.DeadlineExceeded {
		// Deadline exceeded is OK
	} else if err2, ok := err.(net.Error); ok && err2.Timeout() {
		// I/O timeout is OK
	}

	// Now try to receive with a context that has already deadlined.
	_, err = clientConn.ReadMsg(ctx)
	if err == nil {
		t.Errorf("expected error")
	} else if err != context.DeadlineExceeded {
		t.Errorf("expected deadline exceeded")
	}

	// Now try cancellation.
	cancel()
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_, err = clientConn.ReadMsg(ctx)
	if err == nil {
		t.Errorf("expected error but successfully received message")
	} else if err != context.Canceled {
		t.Errorf("unexpected error: %v", err)
	}

	// We do not currently support cancelling read/write calls after they have
	// already been made.

	// Now let the server exit, closing the connection. We should get EOF.
	close(serverCanExitChan)
	for i := 0; i < 3; i++ {
		_, err = clientConn.ReadMsg(context.Background())
		if err == nil {
			t.Errorf("expected error but successfully received message")
		} else if err != io.EOF {
			t.Errorf("unexpected error: %v", err)
		}
	}

	// Test writing to the server after it shuts down
	err = clientConn.WriteMsg(context.Background(), &ircparse.Msg{Command: "FOO"})
	if err == nil {
		t.Errorf("expected error")
	}

	// Close the connection.
	clientConn.Close()

	// Test error codes after closure
	for i := 0; i < 3; i++ {
		_, err = clientConn.ReadMsg(context.Background())
		if err == nil {
			t.Errorf("expected error")
		} else if err == io.EOF {
			// We don't care exactly what error it is, but it shouldn't be an EOF
			t.Errorf("unexpected EOF")
		}
	}

	// Test writing to the server after closure
	err = clientConn.WriteMsg(context.Background(), &ircparse.Msg{Command: "FOO"})
	if err == nil {
		t.Errorf("expected error")
	} else if err != ErrClosed {
		t.Errorf("wrong error %v", err)
	}
}

func genRandomMsg() *ircparse.Msg {
	var b [128]byte
	_, err := rand.Read(b[:])
	if err != nil {
		panic(err)
	}

	L := int(b[len(b)-1])
	if L > len(b) {
		L = len(b)
	}

	return &ircparse.Msg{
		Command: "FOOBAR",
		Args: []string{
			base64.StdEncoding.EncodeToString(b[:][0:L]),
		},
	}
}

// Tests our write deadline handling, which may involve the temporary
// transmission of partial commands.
func TestWriteDeadlines(t *testing.T) {
	defer func() {
		testLimitTxLenFunc = nil
	}()

	clientSide, serverSide := net.Pipe()

	serverCanExitChan := make(chan struct{})
	waitServerExit := make(chan struct{})

	type result struct {
		Msg *ircparse.Msg
		Err error
	}
	expectMsgChan := make(chan result, 128)
	setLimitChan := make(chan int, 1)

	// "server" side
	go func() {
		defer close(waitServerExit)
		serverConn, err := NewConn(context.Background(), serverSide, &Config{})
		if err != nil {
			panic(err)
		}
		defer serverConn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		for {
			select {
			case <-serverCanExitChan:
				return
			case lim := <-setLimitChan:
				testLimitTxLenFunc = func() int { return lim }
			default:
				msg := genRandomMsg()
				err = serverConn.WriteMsg(ctx, msg)
				if err == nil {
					// OK.
					expectMsgChan <- result{msg, err}
				} else if err2, ok := err.(*net.OpError); err == context.DeadlineExceeded || (ok && err2.Timeout()) {
					// Timeout.
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
				} else {
					t.Errorf("write err: %v", err)
				}
			}
		}
	}()

	// client side
	clientConn, err := NewConn(context.Background(), clientSide, &Config{})
	if err != nil {
		panic(err)
	}
	defer clientConn.Close()

loop:
	for j := 0; j < 2; j++ {
		for i := 0; i < 2000; i++ {
			msg, err := clientConn.ReadMsg(context.Background())
			if err != nil {
				t.Errorf("unexpected error %v", err)
			}
			res := <-expectMsgChan
			msg.Raw = ""
			if !reflect.DeepEqual(msg, res.Msg) {
				t.Errorf("mismatch (%d)\n  got     %v\nexpected %v", i, msg, res.Msg)
				break loop
			}
		}
		setLimitChan <- 3
	}

	close(serverCanExitChan)
	<-waitServerExit
}

func TestPing(t *testing.T) {
	testLimitTxLenFunc = func() int { return 3 }
	defer func() {
		testLimitTxLenFunc = nil
	}()

	clientSide, serverSide := net.Pipe()

	go func() {
		serverConn, err := NewConn(context.Background(), serverSide, &Config{})
		if err != nil {
			panic(err)
		}
		defer serverConn.Close()
		for i := 0; i < 100; i++ {
			origMsg := &ircparse.Msg{
				Command: "PING",
				Args:    []string{fmt.Sprintf("X%d", i)},
			}
			err = serverConn.WriteMsg(context.Background(), origMsg)
			if err != nil {
				t.Errorf("error %v", err)
			}
			msg, err := serverConn.ReadMsg(context.Background())
			if err != nil {
				t.Errorf("error %v", err)
			}
			if msg.Command != "PONG" || len(msg.Args) != 1 || msg.Args[0] != origMsg.Args[0] {
				t.Errorf("ping mismatch: %q %+v wanted %q", msg.Command, msg.Args, origMsg.Args[0])
			}
		}
	}()

	clientConn, err := NewConn(context.Background(), clientSide, &Config{})
	if err != nil {
		panic(err)
	}
	defer clientConn.Close()

	for {
		msg, err := clientConn.ReadMsg(context.Background())
		if err != nil {
			break
		}
		if msg.Command != "PING" {
			t.Errorf("expected ping")
		}
	}
}
