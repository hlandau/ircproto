package hdlnick

import (
	"fmt"
	"github.com/hlandau/ircproto/egobot/ircnexus"
	"github.com/hlandau/ircproto/ircneg"
	"github.com/hlandau/ircproto/ircparse"
	"github.com/hlandau/xlog"
	"gopkg.in/hlandau/easyconfig.v1/cflag"
	"regexp"
	"strings"
	"sync"
	"time"
)

var log, Log = xlog.New("hdlnick")

var (
	g            = cflag.NewGroup(nil, "nick")
	passwordFlag = cflag.String(g, "password", "", "services password for your nick")
)

var reAuthDemand = regexp.MustCompile(`(his nickname is registered|seconds to identify to your nickname before|/msg NickServ identify )`)

type handler struct {
	sink          ircnexus.Sink
	once          sync.Once
	preferredNick string

	mutex           sync.RWMutex
	curNick         string
	negCplChan      chan string
	nickChangedChan chan string
	authDemandChan  chan struct{}
	nickInUseChan   chan struct{}
}

var h handler

func (h *handler) OnInit(sink ircnexus.Sink) error {
	h.sink = sink
	h.once.Do(func() { h.initOnce() })
	return nil
}

func (h *handler) initOnce() {
	h.negCplChan = make(chan string)
	h.nickChangedChan = make(chan string)
	h.authDemandChan = make(chan struct{})
	h.nickInUseChan = make(chan struct{}, 1)

	go h.svrLoop()
}

func (h *handler) OnMsg(xmsg *ircnexus.Msg) error {
	if !xmsg.Outgoing && xmsg.Msg.Command == "NICK" && len(xmsg.Msg.Args) > 0 && xmsg.Msg.NickName == h.curNick {
		log.Warnf("our nickname was changed from %q to %q", h.curNick, xmsg.Msg.Args[0])
		h.nickChangedChan <- xmsg.Msg.Args[0]
		h.curNick = xmsg.Msg.Args[0]
	}

	if !xmsg.Outgoing && xmsg.Msg.Command == "NOTICE" && len(xmsg.Msg.Args) >= 2 && xmsg.Msg.Args[0] == h.curNick {
		if reAuthDemand.MatchString(xmsg.Msg.Args[1]) && strings.ToLower(xmsg.Msg.NickName) == "nickserv" {
			log.Warnf("got an authentication demand from %v", xmsg.Msg.NickName)
			h.authDemandChan <- struct{}{}
		}
	}

	if !xmsg.Outgoing && xmsg.Msg.Command == "433" {
		log.Debugf("ALREADY IN USE")
		h.nickInUseChan <- struct{}{}
	}

	return nil
}

func (h *handler) OnNegotiationComplete(res *ircneg.Result) error {
	log.Debugf("we have chosen nick %q", res.ChosenNick)
	h.negCplChan <- res.ChosenNick
	h.curNick = res.ChosenNick
	return nil
}

func (h *handler) svrLoop() {
	lastSentPass := time.Time{}
	period := 10 * time.Second
	periodicChan := time.NewTimer(period)
	defer periodicChan.Stop()

	haveDesiredNick := func() bool { return h.NickName() == h.preferredNick }
	authFailure := false

	for {
		select {
		case chosenNick := <-h.negCplChan:
			authFailure = false
			lastSentPass = time.Time{}
			h.setNickName(chosenNick)
			if !haveDesiredNick() {
				periodicChan.Reset(period)
			} else {
				periodicChan.Stop()
			}

		case newNick := <-h.nickChangedChan:
			lastSentPass = time.Time{}
			if h.NickName() == h.preferredNick && newNick != h.preferredNick {
				// Nick seems to be being changed for us
				authFailure = true
			}
			h.setNickName(newNick)
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
			ircnexus.Write(h.sink, &ircparse.Msg{
				Command: "NICK",
				Args:    []string{h.preferredNick},
			})

		case <-h.nickInUseChan:
			if passwordFlag.Value() != "" {
				ircnexus.Write(h.sink, &ircparse.Msg{
					Command: "PRIVMSG",
					Args:    []string{"NickServ", fmt.Sprintf("REGAIN %s %s", h.preferredNick, passwordFlag.Value())},
				})
			}

		case <-h.authDemandChan:
			if lastSentPass.IsZero() || time.Now().Sub(lastSentPass) >= 5*time.Minute {
				if passwordFlag.Value() != "" {
					ircnexus.Write(h.sink, &ircparse.Msg{
						Command: "PRIVMSG",
						Args:    []string{"NickServ", fmt.Sprintf("IDENTIFY %s", passwordFlag.Value())},
					})
				}
				lastSentPass = time.Now()
			}
		}
	}
}

func (h *handler) NickName() string {
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	return h.curNick
}

func (h *handler) setNickName(nick string) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.curNick = nick
}

func SetPreferredNick(nick string) {
	h.preferredNick = nick
}

func init() {
	ircnexus.Register(&ircnexus.HandlerInfo{
		Handler:     &h,
		Name:        "nick",
		Description: "keeps nickname set correctly",
	})
}
