// Package hdltxbase implements a base transmission driver for the "tx"
// service.
package hdltxbase

import (
	"github.com/hlandau/ircproto/egobot/ircregistry"
)

type handler struct {
	port   ircregistry.Port
	txFunc func(env *ircregistry.Envelope) error
}

func (h *handler) Destroy() error {
	return nil
}

func (h *handler) ListServices() []*ircregistry.ServiceInfo {
	return []*ircregistry.ServiceInfo{
		&ircregistry.ServiceInfo{
			Name:     "tx",
			Priority: 999999000,
		},
	}
}

func (h *handler) OnMsgTx(env *ircregistry.Envelope) error {
	return h.txFunc(env)
}

// Returns a new HandlerInfo which when instantiated by ircregistry, will use
// txFunc to transmit messages. args at instantiation time should be nil.
func Info(txFunc func(env *ircregistry.Envelope) error) *ircregistry.HandlerInfo {
	return &ircregistry.HandlerInfo{
		Name:        "txfinal",
		Description: "Message transmission base module",
		NewFunc: func(port ircregistry.Port) (ircregistry.Handler, error) {
			return &handler{port: port, txFunc: txFunc}, nil
		},
	}
}
