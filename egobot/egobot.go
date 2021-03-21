package main

import (
	"context"
	"io"

	"github.com/hlandau/dexlogconfig"
	"github.com/hlandau/ircproto"
	"github.com/hlandau/ircproto/egobot/hdlautojoin"
	"github.com/hlandau/ircproto/egobot/hdlctcp"
	"github.com/hlandau/ircproto/egobot/hdlhn"
	"github.com/hlandau/ircproto/egobot/hdlnick"
	"github.com/hlandau/ircproto/egobot/hdltxbase"
	"github.com/hlandau/ircproto/egobot/ircregistry"
	"github.com/hlandau/ircproto/ircconn"
	"github.com/hlandau/ircproto/ircneg"
	"github.com/hlandau/ircproto/ircparse"
	"github.com/hlandau/ircproto/ircsasl"
	"github.com/hlandau/xlog"
	"gopkg.in/hlandau/easyconfig.v1"
	"gopkg.in/hlandau/service.v2"
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
	NickPassword      string   `usage:"services nickname password" default:""`
	ServicesNick      string   `usage:"nickname used by nickname services" default:"NickServ"`
	AutojoinChannels  []string `usage:"channels to autojoin" default:""`
	HNTopChannel      string   `usage:"HN top channel" default:""`
	HNLimitItems      int      `usage:"HN item limit" default:"30"`
	HNNotifyNick      string   `usage:"IRC nick to send notifications to" default:""`
	HNNotifyUser      string   `usage:"HN username to notify for" default:""`
	HNNotifyPattern   string   `usage:"regular expression to match HN items on" default:""`
	HNDBPath          string   `usage:"HN database path" default:""`
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
	isConnected := false

	registry, err := ircregistry.New()
	if err != nil {
		return err
	}

	// This handler will execute in the below goroutine even though we register
	// it now.
	err = registry.InstantiateHandler(hdltxbase.Info(func(env *ircregistry.Envelope) error {
		if env.InhibitDelivery {
			return nil
		}

		return bot.client.WriteMsg(context.Background(), env.Msg)
	}), nil)
	if err != nil {
		return err
	}

	// Nickname management.
	err = registry.InstantiateHandler(hdlnick.Info(&hdlnick.Config{
		PreferredNick: bot.cfg.IRCNick,
		Password:      bot.cfg.NickPassword,
		ServicesNick:  bot.cfg.ServicesNick,
	}), nil)
	if err != nil {
		return err
	}

	// CTCP responses.
	err = registry.InstantiateHandler(hdlctcp.Info(&hdlctcp.Config{}), nil)
	if err != nil {
		return err
	}

	// Autojoin.
	err = registry.InstantiateHandler(hdlautojoin.Info(&hdlautojoin.Config{
		Channels: bot.cfg.AutojoinChannels,
	}), nil)
	if err != nil {
		return err
	}

	// HN.
	if bot.cfg.HNTopChannel != "" {
		err = registry.InstantiateHandler(hdlhn.Info(&hdlhn.Config{
			TopChannel:    bot.cfg.HNTopChannel,
			LimitItems:    bot.cfg.HNLimitItems,
			NotifyPattern: bot.cfg.HNNotifyPattern,
			NotifyUser:    bot.cfg.HNNotifyUser,
			NotifyNick:    bot.cfg.HNNotifyNick,
			DBPath:        bot.cfg.HNDBPath,
		}), nil)
		if err != nil {
			return err
		}
	}

	go func() {
		defer close(bot.stoppedChan)
		var negot *ircneg.Result

		for {
			if newNegot := bot.client.NegotiateResult(); newNegot != negot {
				// New connection.
				negot = newNegot
				isConnected = true
				if negot != nil {
					log.Debugf("dispatching negotiation complete")
					err = ircregistry.InjectNegotiationComplete(registry, negot)
					log.Errore(err, "negotiation complete handling")
				}
			}

			msg, err := bot.client.ReadMsg(context.Background())
			if err == ircproto.ErrDisconnected {
				log.Debugf("requested to exit, client shutting down")
				err2 := ircregistry.InjectDisconnected(registry)
				log.Errore(err2, "disconnected handling")
				break
			} else if err == io.EOF {
				// Client disconnected and will reconnect, continue running.
				log.Noticef("client disconnected, reconnecting")
				isConnected = false
				err2 := ircregistry.InjectDisconnected(registry)
				log.Errore(err2, "disconnected handling")
				continue
			} else if err != nil {
				log.Errorf("unexpected error on receive: %v", err)
				continue
			}

			env := &ircregistry.Envelope{
				Msg:      msg,
				Incoming: true,
			}
			err = ircregistry.InjectMsgRx(registry, env)
			log.Errore(err, "RX handling")
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
		DefaultChroot: "/",
		NewFunc: func() (service.Runnable, error) {
			return New(cfg)
		},
	})
}
