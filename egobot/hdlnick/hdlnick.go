// Package hdlnick provides a handler for maintaining an IRC bot's nickname.
package hdlnick

import (
	"fmt"
	"github.com/hlandau/ircproto/egobot/ircregistry"
	"github.com/hlandau/ircproto/ircneg"
	"github.com/hlandau/ircproto/ircparse"
	"github.com/hlandau/xlog"
	"regexp"
	"sync"
	"time"
)

var log, Log = xlog.New("hdlnick")

var reAuthDemand = regexp.MustCompile(`(his nickname is registered|seconds to identify to your nickname before|/msg NickServ identify )`)

type handler struct {
	port ircregistry.Port
	cfg  Config

	negCplChan      chan string
	nickChangedChan chan string
	authDemandChan  chan struct{}
	nickInUseChan   chan struct{}
	disconnectChan  chan struct{}

	mutex   sync.RWMutex
	curNick string // protected by mutex
}

func (h *handler) Destroy() error {
	return nil
}

func (h *handler) ListServices() []*ircregistry.ServiceInfo {
	return []*ircregistry.ServiceInfo{
		&ircregistry.ServiceInfo{
			Name: "rx",
		},
		&ircregistry.ServiceInfo{
			Name: "getnick",
		},
		&ircregistry.ServiceInfo{
			Name: "negotiation-complete",
		},
		&ircregistry.ServiceInfo{
			Name: "disconnected",
		},
	}
}

func (h *handler) OnNegotiationComplete(res *ircneg.Result) error {
	return ircregistry.NextNegotiationComplete(h.port, res, func() error {
		log.Debugf("negotiation complete with chosen nick %q", res.ChosenNick)
		h.setCurNickName(res.ChosenNick)
		h.negCplChan <- res.ChosenNick
		return nil
	})
}

func (h *handler) OnDisconnected() error {
	return ircregistry.NextDisconnected(h.port, func() error {
		h.disconnectChan <- struct{}{}
		return nil
	})
}

func (h *handler) OnMsgRx(env *ircregistry.Envelope) error {
	return ircregistry.NextMsgRx(h.port, env, func() error {
		switch env.Msg.Command {

		case "NICK":
			curNick := h.CurNickName()
			if len(env.Msg.Args) < 1 || !ircparse.NickMatch(env.Msg.NickName, curNick) {
				log.Warnf("nick change does not concern us, %q %q", env.Msg.Args[0], curNick)
				break
			}

			log.Warnf("our nickname was changed from %q to %q", curNick, env.Msg.Args[0])
			h.nickChangedChan <- env.Msg.Args[0]

		case "NOTICE":
			curNick := h.CurNickName()
			if len(env.Msg.Args) < 2 || !ircparse.NickMatch(env.Msg.Args[0], curNick) {
				break
			}

			if reAuthDemand.MatchString(env.Msg.Args[1]) && ircparse.NickMatch(env.Msg.NickName, h.cfg.ServicesNick) {
				log.Noticef("got an authentication demand from %v", env.Msg.NickName)
				h.authDemandChan <- struct{}{}
			}

		case "433", "437":
			h.nickInUseChan <- struct{}{}

		}
		return nil
	})
}

func (h *handler) svrLoop() {
	lastSentPass := time.Time{}
	period := 10 * time.Second
	periodicChan := time.NewTimer(period)
	defer periodicChan.Stop()

	haveDesiredNick := func() bool { return h.CurNickName() == h.cfg.PreferredNick }
	authFailure := false

	for {
		select {
		case chosenNick := <-h.negCplChan:
			authFailure = false
			lastSentPass = time.Time{}
			h.setCurNickName(chosenNick)
			if !haveDesiredNick() {
				periodicChan.Reset(period)
			} else {
				periodicChan.Stop()
			}

		case newNick := <-h.nickChangedChan:
			lastSentPass = time.Time{}
			if haveDesiredNick() && newNick != h.cfg.PreferredNick {
				// Nick seems to be being changed for us
				authFailure = true
			}
			h.setCurNickName(newNick)
			if !haveDesiredNick() {
				if authFailure {
					periodicChan.Reset(24 * time.Hour)
					// Try to REGAIN if possible
					select {
					case h.nickInUseChan <- struct{}{}:
					default:
					}
				} else {
					periodicChan.Reset(1 * time.Second)
				}
			} else {
				authFailure = false
				periodicChan.Stop()
			}

		case <-periodicChan.C:
			if haveDesiredNick() {
				continue
			}

			periodicChan.Reset(period)
			ircregistry.TxCmd(h.port, "NICK", h.cfg.PreferredNick)

		case <-h.nickInUseChan:
			if h.cfg.Password != "" {
				ircregistry.TxCmd(h.port, "PRIVMSG", h.cfg.ServicesNick, fmt.Sprintf("REGAIN %s %s", h.cfg.PreferredNick, h.cfg.Password))
			}

		case <-h.authDemandChan:
			if lastSentPass.IsZero() || time.Now().Sub(lastSentPass) >= 5*time.Minute {
				if h.cfg.Password != "" {
					ircregistry.TxPrivmsg(h.port, h.cfg.ServicesNick, fmt.Sprintf("IDENTIFY %s", h.cfg.Password))
				}
				lastSentPass = time.Now()
			}

		case <-h.disconnectChan:
			periodicChan.Stop()
		}
	}
}

func (h *handler) CurNickName() string {
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	return h.curNick
}

func (h *handler) setCurNickName(nick string) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.curNick = nick
}

// Configuration for the nickname module.
type Config struct {
	PreferredNick string // Preferred nickname.
	Password      string // Services password.
	ServicesNick  string // Nickname services nickname. Defaults to "NickServ".
}

// Creates a new HandlerInfo which, when instantiated in ircregistry, uses the
// given Config to maintain the bot's nickname.
func Info(cfg *Config) *ircregistry.HandlerInfo {
	return &ircregistry.HandlerInfo{
		Name:        "nick",
		Description: "Nickname management and recovery module",
		NewFunc: func(port ircregistry.Port) (ircregistry.Handler, error) {
			h := &handler{
				port: port,
				cfg:  *cfg,

				negCplChan:      make(chan string),
				nickChangedChan: make(chan string),
				authDemandChan:  make(chan struct{}),
				nickInUseChan:   make(chan struct{}, 1),
				disconnectChan:  make(chan struct{}),
			}

			if h.cfg.PreferredNick == "" {
				return nil, fmt.Errorf("must specify preferred nick")
			}

			if h.cfg.ServicesNick == "" {
				h.cfg.ServicesNick = "NickServ"
			}

			go h.svrLoop()

			return h, nil
		},
	}
}
