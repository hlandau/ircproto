package hdlctcp

import (
	"fmt"
	"github.com/hlandau/ircproto/egobot/ircnexus"
	"github.com/hlandau/ircproto/ircparse"
	"github.com/hlandau/xlog"
	"gopkg.in/hlandau/easyconfig.v1/cflag"
	"strings"
	"sync"
)

var log, Log = xlog.New("hdlctcp")

var (
	g           = cflag.NewGroup(nil, "ctcp")
	versionFlag = cflag.String(g, "version", "egobot <sam@devever.net>", "CTCP VERSION response to offer")
)

type handler struct {
	sink ircnexus.Sink
	once sync.Once
}

var h handler

func (h *handler) OnInit(sink ircnexus.Sink) error {
	h.sink = sink
	h.once.Do(func() { h.initOnce() })
	return nil
}

func (h *handler) initOnce() {
}

func (h *handler) OnMsg(xmsg *ircnexus.Msg) error {
	if len(xmsg.Args) < 2 {
		return nil
	}

	if xmsg.Msg.Command != "PRIVMSG" && xmsg.Msg.Command != "NOTICE" {
		return nil
	}

	body := xmsg.Msg.Args[1]
	if len(body) < 2 || body[0] != 1 || body[len(body)-1] != 1 {
		return nil
	}

	body = body[1 : len(body)-1]
	parts := strings.SplitN(body, " ", 2)
	cmdName := parts[0]
	cmdArgs := ""
	if len(parts) > 1 {
		cmdArgs = strings.TrimLeft(parts[1], " ")
	}

	cmdNameUpper := strings.ToUpper(cmdName)

	log.Debugf("CTCP: %q %q", cmdName, cmdArgs)
	switch cmdNameUpper {
	case "VERSION":
		if s := versionFlag.Value(); s != "" {
			ircnexus.Write(h.sink, &ircparse.Msg{
				Command: "PRIVMSG",
				Args: []string{
					xmsg.Msg.NickName,
					fmt.Sprintf("\x01VERSION %s\x01", s),
				},
			})
		}
	}

	return nil
}

func init() {
	ircnexus.Register(&ircnexus.HandlerInfo{
		Handler:     &h,
		Name:        "ctcp",
		Description: "respond to CTCP queries",
	})
}
