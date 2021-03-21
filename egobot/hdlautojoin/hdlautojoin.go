// Package hdlautojoin provides a handler implementing channel autojoin
// functionality.
package hdlautojoin

import (
	"fmt"
	"github.com/hlandau/ircproto/egobot/ircregistry"
	"github.com/hlandau/ircproto/ircneg"
	"github.com/hlandau/ircproto/ircparse"
	"github.com/hlandau/xlog"
	"sync"
	"time"
)

var log, Log = xlog.New("hdlautojoin")

type handler struct {
	port ircregistry.Port
	cfg  Config

	reconnectedChan  chan struct{}
	disconnectedChan chan struct{}
	addChan          chan string
	chanEventChan    chan chanEvent

	joinedChannelsMutex sync.RWMutex
	joinedChannels      map[string]struct{}
}

type chanEventType int

const (
	cetJoining chanEventType = iota
	cetParting
	cetKicked
	cetInvited
	cetInviteOnly
)

type chanEvent struct {
	ChannelName string
	Type        chanEventType
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
			Name: "negotiation-complete",
		},
		&ircregistry.ServiceInfo{
			Name: "disconnected",
		},
		&ircregistry.ServiceInfo{
			Name: "on-channel",
		},
	}
}

func (h *handler) addChannel(channelName string) error {
	if !ircparse.IsValidChannelName(channelName) {
		return fmt.Errorf("malformed channel name, cannot add: %q", channelName)
	}

	h.addChan <- channelName
	return nil
}

func (h *handler) OnMsgRx(env *ircregistry.Envelope) error {
	return ircregistry.NextMsgRx(h.port, env, func() error {
		switch env.Msg.Command {

		case "001":
			log.Debugf("connected, commence autojoin")
			h.reconnectedChan <- struct{}{}

		case "JOIN":
			if len(env.Msg.Args) < 1 || !ircparse.NickMatch(env.Msg.NickName, ircregistry.CurNickName(h.port)) {
				break
			}

			log.Debugf("we are being joined to channel %q", env.Msg.Args[0])
			h.chanEventChan <- chanEvent{ChannelName: env.Msg.Args[0], Type: cetJoining}

		case "PART":
			if len(env.Msg.Args) < 1 || !ircparse.NickMatch(env.Msg.NickName, ircregistry.CurNickName(h.port)) {
				break
			}

			log.Debugf("we are being parted from channel %q", env.Msg.Args[0])
			h.chanEventChan <- chanEvent{ChannelName: env.Msg.Args[0], Type: cetParting}

		case "KICK":
			if len(env.Msg.Args) < 2 || !ircparse.NickMatch(env.Msg.Args[1], ircregistry.CurNickName(h.port)) {
				break
			}

			log.Debugf("we are being kicked from channel %q", env.Msg.Args[0])
			h.chanEventChan <- chanEvent{ChannelName: env.Msg.Args[0], Type: cetKicked}

		case "INVITE":
			if len(env.Msg.Args) < 2 || !ircparse.NickMatch(env.Msg.Args[0], ircregistry.CurNickName(h.port)) {
				break
			}

			log.Debugf("we are being invited to channel %q", env.Msg.Args[1])
			h.chanEventChan <- chanEvent{ChannelName: env.Msg.Args[1], Type: cetInvited}

		case "473":
			// 473 Must be invited (+i)
			if len(env.Msg.Args) < 2 {
				break
			}

			h.chanEventChan <- chanEvent{ChannelName: env.Msg.Args[1], Type: cetInviteOnly}
		}
		return nil
	})
}

func (h *handler) OnNegotiationComplete(res *ircneg.Result) error {
	return ircregistry.NextNegotiationComplete(h.port, res, func() error {
		return nil
	})
}

func (h *handler) OnDisconnected() error {
	return ircregistry.NextDisconnected(h.port, func() error {
		h.disconnectedChan <- struct{}{}
		return nil
	})
}

func (h *handler) IsOnChannel(chanName string) bool {
	normName := ircparse.NormalizeChannelName(chanName)

	h.joinedChannelsMutex.RLock()
	defer h.joinedChannelsMutex.RUnlock()

	_, ok := h.joinedChannels[normName]
	return ok
}

// Autojoin configuration.
type Config struct {
	// Channels to autojoin.
	Channels []string
}

// Creates a new HandlerInfo which, when instantiated in ircregistry, uses the
// given Config to maintain channel joins.
func Info(cfg *Config) *ircregistry.HandlerInfo {
	return &ircregistry.HandlerInfo{
		Name:        "autojoin",
		Description: "Channel autojoin module",
		NewFunc: func(port ircregistry.Port) (ircregistry.Handler, error) {
			h := &handler{
				port:             port,
				cfg:              *cfg,
				reconnectedChan:  make(chan struct{}, 1),
				disconnectedChan: make(chan struct{}, 1),
				addChan:          make(chan string),
				chanEventChan:    make(chan chanEvent),
				joinedChannels:   map[string]struct{}{},
			}

			go h.monLoop()

			for _, channel := range h.cfg.Channels {
				err := h.addChannel(channel)
				if err != nil {
					log.Warnf("cannot add channel: %q: %v", channel, err)
				}
			}

			return h, nil
		},
	}
}

// Monitoring thread.
type monState struct {
	Channels  map[string]*chanInfo
	Connected bool
}

type chanInfo struct {
	Name, NormalizedName string
	Joined               bool
	LastTriedToJoin      time.Time
}

func (h *handler) monLoop() {
	s := monState{
		Channels: map[string]*chanInfo{},
	}
	tickerChan := time.NewTimer(1 * time.Minute)
	defer tickerChan.Stop()

	for {
		select {
		case <-h.reconnectedChan:
			s.Connected = true

			for _, ch := range s.Channels {
				h.monMarkJoined(ch, false)
				ch.LastTriedToJoin = time.Time{}
			}

			h.monJoinChannels(&s)

		case <-h.disconnectedChan:
			s.Connected = false
			// Must clear these too for the sake of IsOnChannel.
			for _, ch := range s.Channels {
				h.monMarkJoined(ch, false)
				ch.LastTriedToJoin = time.Time{}
			}
			tickerChan.Stop()

		case newChanName := <-h.addChan:
			normChanName := ircparse.NormalizeChannelName(newChanName)
			if _, ok := s.Channels[normChanName]; ok {
				continue
			}

			ch := &chanInfo{
				Name:           newChanName,
				NormalizedName: normChanName,
			}
			s.Channels[normChanName] = ch
			tickerChan.Reset(1 * time.Second)

		case ev := <-h.chanEventChan:
			normChanName := ircparse.NormalizeChannelName(ev.ChannelName)
			chanInfo, ok := s.Channels[normChanName]
			if !ok {
				log.Debugf("unknown channel %q (%q)", ev.ChannelName, normChanName)
				break
			}

			switch ev.Type {
			case cetJoining:
				h.monMarkJoined(chanInfo, true)

			case cetParting, cetKicked:
				h.monMarkJoined(chanInfo, false)
				tickerChan.Reset(5 * time.Second)

			case cetInvited:
				// Being invited implies we're not on the channel.
				h.monMarkJoined(chanInfo, false)
				chanInfo.LastTriedToJoin = time.Time{}
				tickerChan.Reset(1 * time.Millisecond)

			case cetInviteOnly:
				h.monKnock(chanInfo.Name)
			}

		case <-tickerChan.C:
			tickerChan.Reset(1 * time.Minute)
			h.monJoinChannels(&s)
		}
	}
}

func (h *handler) monJoinChannels(s *monState) {
	if !s.Connected {
		return
	}

	now := time.Now()
	var names []string
	for _, chanInfo := range s.Channels {
		if chanInfo.Joined || (!chanInfo.LastTriedToJoin.IsZero() && now.Sub(chanInfo.LastTriedToJoin) >= 30*time.Second) {
			continue
		}

		names = append(names, chanInfo.Name)
		chanInfo.LastTriedToJoin = now
	}

	ircregistry.TxCommaSeparated(h.port, "JOIN", names)
}

func (h *handler) monKnock(chanName string) {
	ircregistry.TxCmd(h.port, "KNOCK", chanName, "Bot requesting entry")
}

func (h *handler) monMarkJoined(info *chanInfo, joined bool) {
	info.Joined = joined

	h.joinedChannelsMutex.Lock()
	defer h.joinedChannelsMutex.Unlock()

	if joined {
		h.joinedChannels[info.NormalizedName] = struct{}{}
	} else {
		delete(h.joinedChannels, info.NormalizedName)
	}
}
