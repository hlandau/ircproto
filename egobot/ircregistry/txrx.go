package ircregistry

import (
	"fmt"
	"github.com/hlandau/ircproto/ircneg"
	"github.com/hlandau/ircproto/ircparse"
	"strings"
)

// Wrapper for an ircproto.Message used to pass both the IRC message, and
// information about the message, from handler to handler.
type Envelope struct {
	// The received message.
	Msg *ircparse.Msg

	Incoming        bool // This is an incoming (RX) message, else TX.
	StopHandling    bool // Don't pass this to further handlers (but do to the ultimate client (RX) or server (TX).)
	InhibitDelivery bool // If set, don't pass this to the ultimate client (RX) or server (TX).

	// Escape hatch.
	Values map[interface{}]interface{}
}

// The "rx" service is used to notify handlers of received IRC messages. Each handler
// should pass the message to the next "rx" service handler in a chain, unless it wishes
// to eat the message.
//
// RX events are initially injected into the "rx" handler chain by non-handler
// code which calls GetFirstHandler("rx") on a registry.
type RXService interface {
	OnMsgRx(env *Envelope) error
}

// The "tx" service is used to notify handlers of transmitted IRC messages.
// Each handler should pass the message to the next "tx" service handler in a
// chain, unless it wishes to eat the message. The final "tx" handler is
// generally configured to transmit the message to the network; this handler
// has a very high priority for the "tx" service.
type TXService interface {
	OnMsgTx(env *Envelope) error
}

// The "negotiation-complete" service is used to notify handlers that the
// negotiation sequence of a new IRC connection has been completed. This can be
// used by handlers to determine that the current IRC connection has changed,
// and as a cue to take actions that should be taken at the start of a new
// connection. Each handler should pass the message to the next
// "negotiation-complete" service handler in a chain, unless it wishes to eat
// the message.
type NegotiationCompleteService interface {
	OnNegotiationComplete(res *ircneg.Result) error
}

// The "disconnected" service is used to notify handlers that the connection
// has been disconnected, and that further transmissions will not be possible
// until a negotiation complete event. Each handler should pass the message to
// the next "disconnected" service handler in a chain, unless it wishes to eat
// the message.
type DisconnectedService interface {
	OnDisconnected() error
}

// The "getnick" service is used by handlers to ask other handlers if they know
// what the client's current nickname is.
type GetNickService interface {
	CurNickName() string
}

// Injects an OnMsgRx event into a registry's registered "rx" service chain.
func InjectMsgRx(reg *Registry, env *Envelope) error {
	h := reg.GetFirstHandler("rx")
	if h == nil {
		return nil
	}

	svc, ok := h.(RXService)
	if !ok {
		return fmt.Errorf("first 'rx' handler did not implement interface")
	}

	return svc.OnMsgRx(env)
}

// Injects an OnNegotiationComplete event into a registry's registered
// "negotiation-complete" service chain.
func InjectNegotiationComplete(reg *Registry, res *ircneg.Result) error {
	h := reg.GetFirstHandler("negotiation-complete")
	if h == nil {
		return nil
	}

	svc, ok := h.(NegotiationCompleteService)
	if !ok {
		return fmt.Errorf("first 'negotiation-complete' handler did not implement interface")
	}

	return svc.OnNegotiationComplete(res)
}

// Injects an OnDisconnected event into a registry's registered
// "disconnected" service chain.
func InjectDisconnected(reg *Registry) error {
	h := reg.GetFirstHandler("disconnected")
	if h == nil {
		return nil
	}

	svc, ok := h.(DisconnectedService)
	if !ok {
		return fmt.Errorf("first 'disconnected' handler did not implement interface")
	}

	return svc.OnDisconnected()
}

// Helper function for chaining "rx" service handers. An "rx" service
// implementation should immediately call (only) this function in its OnMsgRx
// implementation. f() will immediately be called which should implement the
// body of the processing; this is expected to be a closure which captures env.
//
// After f() returns, whether or not it returns an error, the next handler in
// the chain is called using the passed port.
func NextMsgRx(port Port, env *Envelope, f func() error) error {
	err := f()
	if env.StopHandling {
		return err
	}

	h := port.GetNextHandler("rx")
	if h == nil {
		return err
	} else if err != nil {
		log.Errorf("error in RX handler: %v", err)
	}

	next, ok := h.(RXService)
	if !ok {
		return fmt.Errorf("expected next handler to implement 'rx' service")
	}

	return next.OnMsgRx(env)
}

// Helper function for chaining "tx" service handlers. A "tx" service
// implementation should immediately call (only) this function in its OnMsgTx
// implementation. f() will immediately be called which should implement the
// body of the processing; this is expected to be a closure which captures env.
//
// After f() returns, whether or not it returns an error, the next handler in
// the chain is called using the passed port.
func NextMsgTx(port Port, env *Envelope, f func() error) error {
	err := f()
	if env.StopHandling {
		return err
	}

	h := port.GetNextHandler("tx")
	if h == nil {
		return err
	} else if err != nil {
		log.Errorf("error in TX handler: %v", err)
	}

	next, ok := h.(TXService)
	if !ok {
		return fmt.Errorf("expected next handler to implement 'tx' service")
	}

	return next.OnMsgTx(env)
}

// Helper function for chaining "negotiation-complete" service handlers. A
// "negotiation-complete" service implementation should immediately call (only)
// this function in its OnNegotiationComplete implementation. f() will
// immediately be called which should implement the body of the processing;
// this is expected to be a closure which captures env.
//
// After f() returns, whether or not it returns an error, the next handler in
// the chain is called using the passed port.
func NextNegotiationComplete(port Port, res *ircneg.Result, f func() error) error {
	err := f()

	h := port.GetNextHandler("negotiation-complete")
	if h == nil {
		return err
	} else if err != nil {
		log.Errorf("error in negotiation complete handler: %v", err)
	}

	next, ok := h.(NegotiationCompleteService)
	if !ok {
		return fmt.Errorf("expected next handler to implement 'negotiation-complete' service")
	}

	return next.OnNegotiationComplete(res)
}

// Helper function for chaining "disconnected" service handlers. A
// "disconnected" service implementation should immediately call (only) this
// function in its OnDisconnected implementation. f() will immediately be
// called which should implement the body of the processing; this is expected
// to be a closure.
//
// After f() returns, whether or not it returns an error, the next handler in
// the chain is called using the passed port.
func NextDisconnected(port Port, f func() error) error {
	err := f()

	h := port.GetNextHandler("disconnected")
	if h == nil {
		return err
	} else if err != nil {
		log.Errorf("error in disconnected handler: %v", err)
	}

	next, ok := h.(DisconnectedService)
	if !ok {
		return fmt.Errorf("expected next handler to implement 'disconnected' service")
	}

	return next.OnDisconnected()
}

// Transmit a new message in a fresh envelope. This function is intended for
// use primarily by handlers.
func TxMsg(port Port, msg *ircparse.Msg) error {
	return NextMsgTx(port, &Envelope{
		Msg: msg,
	}, func() error { return nil })
}

// Transmit a new command. This function is intended for use primarily by handlers.
func TxCmd(port Port, cmdName string, cmdArgs ...string) error {
	return TxMsg(port, &ircparse.Msg{
		Command: cmdName,
		Args:    cmdArgs,
	})
}

// Transmit a new PRIVMSG. This function is intended for use primarily by handlers.
func TxPrivmsg(port Port, tgt, body string) error {
	return TxCmd(port, "PRIVMSG", tgt, body)
}

// Transmit a new NOTICE. This function is intended for use primarily by handlers.
func TxNotice(port Port, tgt, body string) error {
	return TxCmd(port, "NOTICE", tgt, body)
}

// Transmit the given items as a comma separated list in a command with the given name.
// If there are too many items to fit on one command, multiple commands are sent.
func TxCommaSeparated(port Port, cmd string, items []string) error {
	L := 0
	var xs []string
	for _, item := range items {
		if len(cmd)+L+len(item)+1 > 128 {
			err := TxCmd(port, cmd, strings.Join(xs, ","))
			if err != nil {
				return err
			}
			L = 0
			xs = nil
		}
		xs = append(xs, item)
		L += len(item) + 1
	}

	if len(xs) == 0 {
		return nil
	}

	return TxCmd(port, cmd, strings.Join(xs, ","))
}

// Returns the current nickname using the "getnick" service. If the service is
// unavailable or the nickname is unknown, returns "".
func CurNickName(port Port) string {
	h := port.GetNextHandler("getnick")
	if h == nil {
		return ""
	}

	svc, ok := h.(GetNickService)
	if !ok {
		return ""
	}

	return svc.CurNickName()
}
