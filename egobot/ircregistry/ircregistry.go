// Package ircregistry provides a service discovery and registration facility
// for IRC message handling modules.
//
// Each handler exposes zero or more services. Each service has a name.
// Services are consumed by both other handlers, and non-handler code, by
// looking up a service by name to get a Handler interface, and then performing
// an interface upgrade on that Handler interface.
//
// Multiple handlers can be registered as providing the given service. In this
// case, the first (highest priority; lowest numerical priority value) handler
// implementing the service can, if it wishes, chain calls to the methods of
// that service to the next service in the list, and so on.
package ircregistry

import (
	"github.com/hlandau/xlog"
	"sort"
)

var log, Log = xlog.New("ircregistry")

// A registry keeps a register of registered handlers.
type Registry struct {
	handlersByService map[string][]*serviceHandler
}

type serviceHandler struct {
	Service *ServiceInfo
	Port    *handlerPort
}

type handlerPort struct {
	r    *Registry
	h    Handler
	args interface{}
}

func (port *handlerPort) Args() interface{} {
	return port.args
}

func (port *handlerPort) GetNextHandler(service string) Handler {
	return port.r.getNextHandler(port, service)
}

// Instantiates a new registry.
func New() (*Registry, error) {
	return &Registry{
		handlersByService: map[string][]*serviceHandler{},
	}, nil
}

// Gets the first handler for a given service. Returns nil if no
// handler is registered for that service.
func (r *Registry) GetFirstHandler(serviceName string) Handler {
	svc, ok := r.handlersByService[serviceName]
	if !ok {
		return nil
	}

	return svc[0].Port.h
}

func (r *Registry) getNextHandler(port *handlerPort, service string) Handler {
	list, _ := r.handlersByService[service]
	for i := range list {
		if list[i].Port == port {
			if i == len(list)-1 {
				return nil
			}
			return list[i+1].Port.h
		}
	}

	return list[0].Port.h
}

// Instantiates a handler using the given HandlerInfo. The args argument is
// optional and made available via the Args() method on the port passed to the
// newly instantiated handler.
func (r *Registry) InstantiateHandler(hi *HandlerInfo, args interface{}) error {
	port := &handlerPort{r: r, args: args}

	h, err := hi.NewFunc(port)
	if err != nil {
		return err
	}

	port.h = h

	svcs := h.ListServices()
	for _, svc := range svcs {
		r.registerService(svc, port)
	}

	return nil
}

func (r *Registry) registerService(svc *ServiceInfo, port *handlerPort) {
	sh := &serviceHandler{
		Service: svc,
		Port:    port,
	}
	list, _ := r.handlersByService[svc.Name]

	list = append(list, sh)

	sort.SliceStable(list, func(i, j int) bool {
		return list[i].Service.Priority < list[j].Service.Priority
	})
	r.handlersByService[svc.Name] = list
}

// All handlers must implement this interface.
type Handler interface {
	Destroy() error

	// Called immediately after handler instantiation to get a list of services
	// implemented by the handler. The handler is registered as providing the
	// given service, and each service is registered according to the priority
	// specified, which determines its order in the list of handlers providing
	// that service.
	ListServices() []*ServiceInfo
}

// When a handler is instantiated, it is passed a Port, which provides a
// handler-specific interface to the registry. It should not be used by
// handlers other than the handler which received it.
type Port interface {
	// Returns the value of args passed to InstantiateHandler.
	Args() interface{}

	// If the handler whose port this is provides the given service, returns the
	// handler immediately following it in the list for the given service.
	// Otherwise, it returns the first handler for the given service. If no
	// handler is registered for the service or this handler is the last handler
	// in the list for the given service, returns nil.
	GetNextHandler(service string) Handler
}

// Information which can be used to instantiate a handler. A *HandlerInfo
// global should be provided by each handler implementation to allow it to be
// instantiated.
type HandlerInfo struct {
	// A short name of the handler. Should be lowercase without spaces.
	Name string

	// A single-line description of the handler.
	Description string

	// Function used to instantiate the handler. It is passed a handler-specific
	// port interface which the handler should note.
	NewFunc func(port Port) (Handler, error)
}

// Information about a service implemented by a handler.
type ServiceInfo struct {
	// The name of the service implemented.
	Name string

	// Service priority. Zero is a sensible default. This determines the ordering
	// of the handler in the list of handlers implementing this service.
	Priority int
}
