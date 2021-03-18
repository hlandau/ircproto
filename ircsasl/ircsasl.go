// Package ircsasl provides a basic SASL implementation for IRCv3 usage.
package ircsasl

import (
	"encoding/base64"
	"fmt"
	"sort"
)

type Credentials struct {
	Username      string
	AuthzUsername string
	Password      string
}

type Config struct {
	Credentials      Credentials
	AvailableMethods []string
}

type Engine struct {
	cfg          Config
	curMethodIdx int
}

// Instantiates a SASL engine, which is a SASL state machine to be used in the
// context of a given session.
func NewEngine(cfg *Config) (*Engine, error) {
	e := &Engine{
		cfg: *cfg,
	}

	err := e.SetMethods(e.cfg.AvailableMethods)
	if err != nil {
		return nil, err
	}

	return e, nil
}

func (e *Engine) isMethodSupported(method string) bool {
	switch method {
	case "PLAIN", "EXTERNAL":
		return true
	default:
		return false
	}
}

func (e *Engine) methodPref(method string) int {
	switch method {
	case "EXTERNAL":
		return 0
	case "PLAIN":
		return 1
	default:
		return 9999
	}
}

// If, after initially attempting authentication, more information is obtained
// on the available methods (for example, because the server rejected an
// attempt to initiate a different method and provided a list of supported
// methods), this method should be called. Note that this resets the state
// machine.
func (e *Engine) SetMethods(methods []string) error {
	var ms []string
	for _, method := range methods {
		if e.isMethodSupported(method) {
			ms = append(ms, method)
		}
	}

	sort.SliceStable(ms, func(i, j int) bool {
		prefI := e.methodPref(ms[i])
		prefJ := e.methodPref(ms[j])
		return prefI < prefJ
	})

	e.cfg.AvailableMethods = ms
	e.curMethodIdx = -1
	return nil
}

// Call to begin attempting to authenticate with the next method on our list.
// Returns the method name which is initiated. This method name should be sent
// to the server.
func (e *Engine) BeginMethod() (string, error) {
	if e.curMethodIdx+1 >= len(e.cfg.AvailableMethods) {
		return "", fmt.Errorf("all available SASL authentication methods exhausted")
	}

	e.curMethodIdx++
	return e.cfg.AvailableMethods[e.curMethodIdx], nil
}

// Advances the SASL state machine. data is (usually base64-encoded) data which
// has been provided by the server. The return value is (usually
// base64-encoded) data to be submitted to the server.
func (e *Engine) Stimulus(data string) (string, error) {
	curMethod := e.cfg.AvailableMethods[e.curMethodIdx]
	switch curMethod {
	case "PLAIN":
		return e.szPlain()
	case "EXTERNAL":
		return e.szExternal()
	default:
		panic("unsupported method")
	}
}

func (e *Engine) szPlain() (string, error) {
	var b []byte
	b = append(b, []byte(e.cfg.Credentials.AuthzUsername)...)
	b = append(b, 0)
	b = append(b, []byte(e.cfg.Credentials.Username)...)
	b = append(b, 0)
	b = append(b, []byte(e.cfg.Credentials.Password)...)

	return base64.StdEncoding.EncodeToString(b), nil
}

func (e *Engine) szExternal() (string, error) {
	user := e.cfg.Credentials.AuthzUsername
	if user == "" {
		user = e.cfg.Credentials.Username
	}
	return base64.StdEncoding.EncodeToString([]byte(user)), nil
}
