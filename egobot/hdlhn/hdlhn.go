// Package hdlhn provides Hacker News tailing functionality.
package hdlhn

import (
	"fmt"
	"github.com/hlandau/ircproto/egobot/hnapi"
	"github.com/hlandau/ircproto/egobot/hntail"
	"github.com/hlandau/ircproto/egobot/ircregistry"
	"github.com/hlandau/xlog"
	"regexp"
	"strings"
	"sync"
	"time"
)

var log, Log = xlog.New("hdlhn")

type handler struct {
	port     ircregistry.Port
	cfg      Config
	c        *hnapi.Client
	tailer   *hntail.Tailer
	reNotify *regexp.Regexp

	watchingParents      map[int]time.Time
	watchingParentsMutex sync.Mutex
}

func (h *handler) Destroy() error {
	return nil
}

func (h *handler) ListServices() []*ircregistry.ServiceInfo {
	return []*ircregistry.ServiceInfo{}
}

func (h *handler) onNewTopItem(itemNo int) error {
	if !ircregistry.IsOnChannel(h.port, h.cfg.TopChannel) {
		log.Errorf("cannot send new item: not on channel %q", h.cfg.TopChannel)
		return fmt.Errorf("not on channel")
	}

	item, err := h.c.GetItem(itemNo)
	if err != nil {
		log.Errore(err, "failed to fetch HN item")
		return err
	}

	err = ircregistry.TxPrivmsg(h.port, h.cfg.TopChannel, formatItem(item))
	if err != nil {
		log.Errore(err, "failed to send HN message to channel")
		return nil
	}

	time.Sleep(1 * time.Second)
	return nil
}

func (h *handler) onNewItem(itemNo int) error {
	item, err := h.c.GetItem(itemNo)
	if err != nil {
		log.Errore(err, "failed to fetch HN item")
		return err
	}

	log.Debugf("%+v", item)
	h.auditItem(item)

	return nil
}

func (h *handler) auditItem(item *hnapi.Item) {
	titleMatches, textMatches, urlMatches := false, false, false
	if h.reNotify != nil {
		titleMatches = h.reNotify.MatchString(item.Title)
		textMatches = h.reNotify.MatchString(item.Text)
		urlMatches = h.reNotify.MatchString(item.URL)
	}
	authorMatches := (h.cfg.NotifyUser != "" && item.By == h.cfg.NotifyUser)
	parentMatches := h.isWatching(item.Parent)
	if !titleMatches && !textMatches && !urlMatches && !parentMatches && !authorMatches {
		return
	}

	h.markWatching(item.ID)

	msg := h.formatNotifyItem(item)
	log.Noticef("%s", msg)

	if h.cfg.NotifyNick != "" {
		err := ircregistry.TxPrivmsg(h.port, h.cfg.NotifyNick, msg)
		if err != nil {
			log.Errore(err, "failed to send notify privmsg")
		}
	}
}

func formatItem(item *hnapi.Item) string {
	hnURL := fmt.Sprintf("https://news.ycombinator.com/item?id=%v", item.ID)
	url := item.URL
	if url != "" {
		url = "\x1F" + url + "\x1F "
	}
	return fmt.Sprintf("\x02\x0307%s\x0399\x02 \x0314[%d %s]\x0399 %s%s", item.Title, item.Score, item.By, url, hnURL)
}

func (h *handler) formatNotifyItem(item *hnapi.Item) string {
	hnURL := fmt.Sprintf("https://news.ycombinator.com/item?id=%v", item.ID)

	s := fmt.Sprintf("HN %s: %s ", item.Type, hnURL)
	if item.Title != "" {
		s += fmt.Sprintf("%q: ", item.Title)
	}
	if item.URL != "" {
		s += item.URL
	} else {
		xs := h.reNotify.FindAllString(item.Text, -1)
		s += strings.Join(xs, ", ")
	}
	return s
}

func (h *handler) markWatching(itemNo int) {
	h.watchingParentsMutex.Lock()
	defer h.watchingParentsMutex.Unlock()
	h.watchingParents[itemNo] = time.Now()
}

func (h *handler) isWatching(itemNo int) bool {
	h.watchingParentsMutex.Lock()
	defer h.watchingParentsMutex.Unlock()
	_, ok := h.watchingParents[itemNo]
	return ok
}

func (h *handler) cleanLoop() {
	for {
		time.Sleep(10 * time.Minute)
		h.clean()
	}
}

func (h *handler) clean() {
	h.watchingParentsMutex.Lock()
	defer h.watchingParentsMutex.Unlock()

	now := time.Now()
	var cleanIDs []int
	for id, t := range h.watchingParents {
		if now.Sub(t) >= 5*24*time.Hour {
			cleanIDs = append(cleanIDs, id)
		}
	}

	for _, id := range cleanIDs {
		delete(h.watchingParents, id)
	}
}

type Config struct {
	API           hnapi.Config
	TopChannel    string
	LimitItems    int
	NotifyPattern string
	NotifyUser    string
	NotifyNick    string
}

func Info(cfg *Config) *ircregistry.HandlerInfo {
	return &ircregistry.HandlerInfo{
		Name:        "hntail",
		Description: "HN tailing module",
		NewFunc: func(port ircregistry.Port) (ircregistry.Handler, error) {
			h := &handler{
				port:            port,
				cfg:             *cfg,
				watchingParents: map[int]time.Time{},
			}

			if h.cfg.API.URL == "" {
				h.cfg.API.URL = "https://hacker-news.firebaseio.com/v0/"
			}

			var err error
			if h.cfg.NotifyPattern != "" {
				h.reNotify, err = regexp.Compile(h.cfg.NotifyPattern)
				if err != nil {
					return nil, err
				}
			}

			h.c, err = hnapi.New(&h.cfg.API)
			if err != nil {
				return nil, err
			}

			h.tailer, err = hntail.New(&hntail.Config{
				Client:     h.c,
				LimitItems: h.cfg.LimitItems,
				OnNewTopItem: func(itemNo int) error {
					return h.onNewTopItem(itemNo)
				},
				OnNewItem: func(itemNo int) error {
					return h.onNewItem(itemNo)
				},
			})
			if err != nil {
				return nil, err
			}

			go h.cleanLoop()

			return h, nil
		},
	}
}
