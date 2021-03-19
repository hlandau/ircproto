package main

import (
	"context"
	"fmt"
	"io"

	"github.com/hlandau/dexlogconfig"
	"github.com/hlandau/ircproto"
	"github.com/hlandau/ircproto/egobot/ircnexus"
	"github.com/hlandau/ircproto/ircconn"
	"github.com/hlandau/ircproto/ircneg"
	"github.com/hlandau/ircproto/ircparse"
	"github.com/hlandau/ircproto/ircsasl"
	"github.com/hlandau/xlog"
	"gopkg.in/hlandau/easyconfig.v1"
	"gopkg.in/hlandau/service.v2"

	_ "github.com/hlandau/ircproto/egobot/hdlautojoin"
	"github.com/hlandau/ircproto/egobot/hdlnick"
)

var log, Log = xlog.New("egobot")

type Config struct {
	IRCUser           string   `usage:"IRC username" default:"egobot"`
	IRCNick           string   `usage:"IRC nickname" default:""`
	IRCReal           string   `usage:"IRC realname" default:"egobot"`
	IRCURLs           []string `usage:"IRC URLs" default:""`
	IRCSASLUsername   string   `usage:"IRC SASL username" default:""`
	IRCSASLPassword   string   `usage:"IRC SASL password" default:""`
	IRCServerPassword string   `usage:"IRC server password" default:""`
}

type Bot struct {
	cfg         Config
	client      *ircproto.Client
	stoppedChan chan struct{}
}

func New(cfg *Config) (*Bot, error) {
	bot := &Bot{
		cfg:         *cfg,
		stoppedChan: make(chan struct{}),
	}

	return bot, nil
}

func (bot *Bot) Start() error {
	client, err := ircproto.New(ircproto.Simple(&ircneg.Config{
		IRCUser:     bot.cfg.IRCUser,
		IRCNick:     []string{bot.cfg.IRCNick},
		IRCReal:     bot.cfg.IRCReal,
		DesiredCaps: []string{"server-time", "account-tag", "away-notify", "extended-join", "invite-notify", "multi-prefix", "userhost-in-names", "chghost"},
		SASLCredentials: ircsasl.Credentials{
			Username: bot.cfg.IRCSASLUsername,
			Password: bot.cfg.IRCSASLPassword,
		},
	}, bot.cfg.IRCURLs...).WithConn(&ircconn.Config{
		LogWriteFunc: func(msg *ircparse.Msg) {
			s, _ := msg.String()
			log.Debugf("TX: %q", s)
		},
		LogReadFunc: func(msg *ircparse.Msg) {
			s, _ := msg.String()
			log.Debugf("RX: %q", s)
		},
	}))
	if err != nil {
		return err
	}

	bot.client = client
	hdlnick.SetPreferredNick(bot.cfg.IRCNick)

	ircnexus.Init(ircnexus.SinkFunc(func(xmsg *ircnexus.Msg) error {
		if !xmsg.Outgoing {
			return fmt.Errorf("not an outgoing message")
		}
		if xmsg.InhibitDelivery {
			return nil
		}
		return client.WriteMsg(context.Background(), xmsg.Msg)
	}))

	go func() {
		defer close(bot.stoppedChan)
		negot := bot.client.NegotiateResult()

		for {
			if newNegot := bot.client.NegotiateResult(); newNegot != negot {
				// New connection.
				negot = newNegot
				if negot != nil {
					ircnexus.HandleNegotiationComplete(negot)
				}
			}

			msg, err := bot.client.ReadMsg(context.Background())
			if err == ircproto.ErrDisconnected {
				log.Debugf("requested to exit, client shutting down")
				break
			} else if err == io.EOF {
				// Client disconnected and will reconnect, continue running.
				log.Noticef("client disconnected, reconnecting")
				continue
			} else if err != nil {
				log.Errorf("unexpected error on receive: %v", err)
				continue
			}

			xmsg := &ircnexus.Msg{
				Msg:      msg,
				Outgoing: false,
			}

			ircnexus.Handle(xmsg)
		}
	}()

	return nil
}

func (bot *Bot) Stop() error {
	log.Debugf("stopping")
	bot.client.Close()
	<-bot.stoppedChan
	return nil
}

func main() {
	cfg := &Config{}
	config := easyconfig.Configurator{
		ProgramName: "egobot",
	}
	config.ParseFatal(cfg)
	dexlogconfig.Init()

	service.Main(&service.Info{
		Name:          "egobot",
		Description:   "egobot IRC bot",
		DefaultChroot: service.EmptyChrootPath,
		NewFunc: func() (service.Runnable, error) {
			return New(cfg)
		},
	})
}
