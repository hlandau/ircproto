// Package hdlctcp provides CTCP VERSION responder functionality.
package hdlctcp

import (
	"fmt"
	"github.com/hlandau/ircproto/egobot/ircregistry"
	"github.com/hlandau/ircproto/ircparse"
	"github.com/hlandau/xlog"
	"strings"
)

var log, Log = xlog.New("hdlctcp")

type handler struct {
	port ircregistry.Port
	cfg  Config
}

func (h *handler) Destroy() error {
	return nil
}

func (h *handler) ListServices() []*ircregistry.ServiceInfo {
	return []*ircregistry.ServiceInfo{
		&ircregistry.ServiceInfo{
			Name: "rx",
		},
	}
}

func (h *handler) OnMsgRx(env *ircregistry.Envelope) error {
	return ircregistry.NextMsgRx(h.port, env, func() error {
		switch env.Msg.Command {

		case "PRIVMSG", "NOTICE":
			curNick := ircregistry.CurNickName(h.port)
			if len(env.Msg.Args) < 2 || !ircparse.NickMatch(env.Msg.Args[0], curNick) {
				break
			}

			return h.onCtcp(env, env.Msg.Args[1])
		}
		return nil
	})
}

func (h *handler) onCtcp(env *ircregistry.Envelope, body string) error {
	// Determine if this is CTCP.
	if len(body) < 2 {
		return nil
	}

	if body[0] != 1 || body[len(body)-1] != 1 {
		return nil
	}

	// Get command.
	body = body[1 : len(body)-1]
	parts := strings.SplitN(body, " ", 2)
	cmdName := parts[0]
	cmdArgs := ""
	if len(parts) > 1 {
		cmdArgs = parts[1]
	}

	cmdNameUpper := strings.ToUpper(cmdName)
	log.Debugf("CTCP: %q %q", cmdNameUpper, cmdArgs)
	switch cmdNameUpper {
	case "VERSION":
		if h.cfg.VersionResponse != "" {
			env.StopHandling = true
			ircregistry.TxNotice(h.port, env.Msg.NickName, fmt.Sprintf("\x01VERSION %s\x01", h.cfg.VersionResponse))
		}
	}

	return nil
}

type Config struct {
	VersionResponse string // String to reply with to CTCP VERSION requests.
}

func Info(cfg *Config) *ircregistry.HandlerInfo {
	return &ircregistry.HandlerInfo{
		Name:        "ctcp",
		Description: "CTCP request handling module",
		NewFunc: func(port ircregistry.Port) (ircregistry.Handler, error) {
			h := &handler{
				port: port,
				cfg:  *cfg,
			}

			if h.cfg.VersionResponse == "" {
				h.cfg.VersionResponse = "egobot (sam@devever.net)"
			}

			return h, nil
		},
	}
}
