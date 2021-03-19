package hdlautojoin

import (
	"fmt"
	"github.com/hlandau/ircproto/egobot/ircnexus"
	"github.com/hlandau/ircproto/ircparse"
	"github.com/hlandau/xlog"
	"gopkg.in/hlandau/easyconfig.v1/cflag"
	"regexp"
	"strings"
	"sync"
	"time"
)

var log, Log = xlog.New("hdlautojoin")

var (
	g            = cflag.NewGroup(nil, "autojoin")
	channelsFlag = cflag.String(g, "channels", "", "channels to join")
)

type chanInfo struct {
	Name, NormalizedName string
	Joined               bool
	LastTriedToJoin      time.Time
}

type handler struct {
	sink ircnexus.Sink
	once sync.Once

	reconnectedChan chan struct{}
	addChan         chan string
	chanEventChan   chan chanEvent
}

var h handler

var reChannelName = regexp.MustCompile(`^[^,0-9 \r\n][^, \r\n]{0,128}$`)

func (h *handler) OnInit(sink ircnexus.Sink) error {
	h.sink = sink
	h.once.Do(func() { h.initOnce() })
	return nil
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

func (h *handler) initOnce() {
	h.reconnectedChan = make(chan struct{}, 1)
	h.addChan = make(chan string)
	h.chanEventChan = make(chan chanEvent)

	go h.monLoop()

	for _, channel := range strings.Split(channelsFlag.Value(), ",") {
		err := h.addChannel(channel)
		if err != nil {
			log.Warnf("cannot add channel: %v", err)
		}
	}
}

func (h *handler) addChannel(channelName string) error {
	if !reChannelName.MatchString(channelName) {
		return fmt.Errorf("malformed channel name, cannot add: %q", channelName)
	}

	h.addChan <- channelName
	return nil
}

func (h *handler) OnMsg(xmsg *ircnexus.Msg) error {
	if !xmsg.Outgoing && xmsg.Msg.Command == "001" {
		log.Debugf("got 001, autojoin")
		h.reconnectedChan <- struct{}{}
	}

	if !xmsg.Outgoing && xmsg.Msg.Command == "JOIN" && len(xmsg.Msg.Args) > 0 && xmsg.Msg.NickName == getCurNickName() {
		log.Debugf("we are being joined to channel %q", xmsg.Msg.Args[0])
		h.chanEventChan <- chanEvent{ChannelName: xmsg.Msg.Args[0], Type: cetJoining}
	}

	if !xmsg.Outgoing && xmsg.Msg.Command == "PART" && len(xmsg.Msg.Args) > 0 && xmsg.Msg.NickName == getCurNickName() {
		log.Debugf("we are being parted from channel %q", xmsg.Msg.Args[0])
		h.chanEventChan <- chanEvent{ChannelName: xmsg.Msg.Args[0], Type: cetParting}
	}

	if !xmsg.Outgoing && xmsg.Msg.Command == "KICK" && len(xmsg.Msg.Args) >= 2 && xmsg.Msg.Args[1] == getCurNickName() {
		log.Debugf("we are being kicked from channel %q", xmsg.Msg.Args[0])
		h.chanEventChan <- chanEvent{ChannelName: xmsg.Msg.Args[0], Type: cetKicked}
	}

	if !xmsg.Outgoing && xmsg.Msg.Command == "INVITE" && len(xmsg.Msg.Args) >= 2 && xmsg.Msg.Args[0] == getCurNickName() {
		log.Debugf("we are being invited to channel %q", xmsg.Msg.Args[1])
		h.chanEventChan <- chanEvent{ChannelName: xmsg.Msg.Args[1], Type: cetInvited}
	}

	if !xmsg.Outgoing && xmsg.Msg.Command == "473" && len(xmsg.Msg.Args) >= 2 {
		// 473 Must be invited (+i)
		h.chanEventChan <- chanEvent{ChannelName: xmsg.Msg.Args[1], Type: cetInviteOnly}
	}

	return nil
}

func (h *handler) monLoop() {
	chanInfoMap := map[string]*chanInfo{}
	tickerChan := time.NewTimer(1 * time.Minute)
	defer tickerChan.Stop()

	for {
		select {
		case <-h.reconnectedChan:
			for _, ch := range chanInfoMap {
				ch.Joined = false
				ch.LastTriedToJoin = time.Time{}
			}

			h.monJoinChannels(chanInfoMap)

		case newChanName := <-h.addChan:
			normChanName := normalizeChanName(newChanName)
			if _, ok := chanInfoMap[normChanName]; ok {
				continue
			}

			ch := &chanInfo{
				Name:           newChanName,
				NormalizedName: normChanName,
			}
			chanInfoMap[normChanName] = ch

		case ev := <-h.chanEventChan:
			normChanName := normalizeChanName(ev.ChannelName)
			chanInfo, ok := chanInfoMap[normChanName]
			if !ok {
				log.Debugf("unknown channel %q (%q)", ev.ChannelName, normChanName)
			}
			switch ev.Type {
			case cetJoining:
				if ok {
					chanInfo.Joined = true
				}

			case cetParting, cetKicked:
				if ok {
					chanInfo.Joined = false
					tickerChan.Reset(5 * time.Second)
				}

			case cetInvited:
				if ok {
					chanInfo.Joined = false // being invited implies we're not on the channel
					chanInfo.LastTriedToJoin = time.Time{}
					tickerChan.Reset(1 * time.Millisecond)
				}

			case cetInviteOnly:
				if ok {
					h.monKnock(chanInfo.Name)
				}
			}

		case <-tickerChan.C:
			tickerChan.Reset(1 * time.Minute)
			h.monJoinChannels(chanInfoMap)
		}
	}
}

func (h *handler) monJoinChannels(chanInfoMap map[string]*chanInfo) {
	var chans []string
	L := 0
	now := time.Now()
	log.Debugf("checking joinable channels")
	for _, chanInfo := range chanInfoMap {
		if chanInfo.Joined || (!chanInfo.LastTriedToJoin.IsZero() && now.Sub(chanInfo.LastTriedToJoin) >= 30*time.Second) {
			continue
		}

		if L+1+len(chanInfo.Name) > 128 {
			ircnexus.Write(h.sink, &ircparse.Msg{
				Command: "JOIN",
				Args:    []string{strings.Join(chans, ",")},
			})
			L = 0
			chans = nil
		}
		chans = append(chans, chanInfo.Name)
		L += len(chanInfo.Name) + 1

		chanInfo.LastTriedToJoin = now
	}

	if len(chans) > 0 {
		ircnexus.Write(h.sink, &ircparse.Msg{
			Command: "JOIN",
			Args:    []string{strings.Join(chans, ",")},
		})
	}
}

func (h *handler) monKnock(chanName string) {
	ircnexus.Write(h.sink, &ircparse.Msg{
		Command: "KNOCK",
		Args:    []string{chanName, "Bot requesting entry"},
	})
}

func getCurNickName() string {
	if h2, ok := ircnexus.GetHandler("nick").(interface{ NickName() string }); ok {
		return h2.NickName()
	}

	panic("nick not available")
}

func normalizeChanName(chanName string) string {
	return strings.ToLower(chanName)
}

func init() {
	ircnexus.Register(&ircnexus.HandlerInfo{
		Handler:     &h,
		Name:        "autojoin",
		Description: "automatically joins channels",
	})
}
