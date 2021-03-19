package ircnexus

import (
	"github.com/hlandau/ircproto/ircneg"
	"github.com/hlandau/ircproto/ircparse"
	"github.com/hlandau/xlog"
	"sort"
	"sync"
)

var log, Log = xlog.New("ircnexus")

// Extended IRC message. Builds on ircparse.Msg to allow annotation with metadata to be passed between handlers.
type Msg struct {
	*ircparse.Msg

	Outgoing        bool // This is an outgoing (TX) message, else incoming (RX).
	StopHandling    bool // Don't pass this to further handles (but do to the ultimate client (RX) or server (TX)).
	InhibitDelivery bool // Don't pass this to the ultimate client (RX) or server (TX).

	// Escape hatch.
	Metadata map[interface{}]interface{}
}

// Sink used by handlers to transmit messages.
type Sink interface {
	WriteMsg(msg *Msg) error
}

// Simple function implementing Sink.
type SinkFunc func(msg *Msg) error

func (sf SinkFunc) WriteMsg(msg *Msg) error {
	return sf(msg)
}

// Helper function for sinks.
func Write(sink Sink, msg *ircparse.Msg) error {
	xmsg := &Msg{
		Msg:      msg,
		Outgoing: true,
	}
	return sink.WriteMsg(xmsg)
}

// The minimal interface that a handler must implement.
type Handler interface {
	OnInit(sink Sink) error
	OnMsg(msg *Msg) error
}

type HandlerNegotiationComplete interface {
	OnNegotiationComplete(res *ircneg.Result) error
}

// Information about a handler to be registered.
type HandlerInfo struct {
	Handler     Handler // The handler interface.
	Name        string  // Name of the handler. Should be lowercase without spaces.
	Description string  // Brief one-line description of the handler.
	Priority    int     // Sort priority for message handling.
}

var handlerMutex sync.RWMutex
var handlers []*HandlerInfo
var handlersByName = map[string]*HandlerInfo{}

func getHandlers() []*HandlerInfo {
	handlerMutex.RLock()
	defer handlerMutex.RUnlock()
	return handlers
}

func getHandlerByName(name string) *HandlerInfo {
	handlerMutex.RLock()
	defer handlerMutex.RUnlock()
	hi, _ := handlersByName[name]
	return hi
}

func setHandlers(newHandlers []*HandlerInfo) {
	handlerMutex.Lock()
	defer handlerMutex.Unlock()
	byName := map[string]*HandlerInfo{}

	for _, h := range newHandlers {
		byName[h.Name] = h
	}

	handlers = newHandlers
	handlersByName = byName
}

// Register a new handler.
func Register(info *HandlerInfo) {
	ourInfo := *info // copy

	// Always make a new slice.
	var newHandlers []*HandlerInfo
	newHandlers = append(newHandlers, getHandlers()...)
	newHandlers = append(handlers, &ourInfo)

	sort.SliceStable(newHandlers, func(i, j int) bool {
		return newHandlers[i].Priority < newHandlers[j].Priority
	})

	setHandlers(newHandlers)
}

// Unregister a handler by name.
func UnregisterByName(handlerName string) bool {
	var newHandlers []*HandlerInfo
	found := false

	for _, handler := range getHandlers() {
		if handler.Name != handlerName {
			newHandlers = append(newHandlers, handler)
		} else {
			found = true
		}
	}

	if found {
		setHandlers(newHandlers)
	}

	return found
}

// Inject sink into handlers.
func Init(sink Sink) {
	for _, handler := range getHandlers() {
		err := handler.Handler.OnInit(sink)
		if err != nil {
			log.Errorf("error in handler %q while injecting sink: %v", handler.Name, err)
			// But continue.
		}
	}
}

// Run an RX message through the registered handlers.
func Handle(msg *Msg) {
	for _, handler := range getHandlers() {
		if msg.StopHandling {
			break
		}

		err := handler.Handler.OnMsg(msg)
		if err != nil {
			log.Errorf("error in handler %q while handling RX msg: %v", handler.Name, err)
			// But continue passing to the other handlers.
		}
	}
}

func HandleNegotiationComplete(res *ircneg.Result) {
	for _, handler := range getHandlers() {
		h, ok := handler.Handler.(HandlerNegotiationComplete)
		if !ok {
			continue
		}

		err := h.OnNegotiationComplete(res)
		if err != nil {
			log.Errorf("error in handler %q while handling negotiation complete event: %v", handler.Name, err)
			// But continue passing to the other handlers.
		}
	}
}

func GetHandler(name string) Handler {
	hi := getHandlerByName(name)
	if hi == nil {
		return nil
	}
	return hi.Handler
}
