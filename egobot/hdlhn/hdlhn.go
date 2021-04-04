// Package hdlhn provides Hacker News tailing functionality.
package hdlhn

import (
	"encoding/binary"
	"fmt"
	"github.com/boltdb/bolt"
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

	mostRecentProcessed      int
	mostRecentProcessedMutex sync.Mutex
	mostRecentProcessedChan  chan struct{}

	db *bolt.DB
}

func (h *handler) Destroy() error {
	return nil
}

func (h *handler) ListServices() []*ircregistry.ServiceInfo {
	return []*ircregistry.ServiceInfo{}
}

func (h *handler) onNewTopItem(itemNo int) error {
	if !ircregistry.IsOnChannel(h.port, h.cfg.TopChannel) {
		log.Errorf("cannot send new top item: not on channel %q", h.cfg.TopChannel)
		return fmt.Errorf("not on channel")
	}

	item, err := h.getItem(itemNo)
	if err != nil {
		if err2, ok := err.(*hnapi.FetchError); ok && !err2.Temporary() {
			log.Errore(err, "failed to fetch HN item - non-temporary error, ignoring this item")
			return nil
		}
		log.Errore(err, "failed to fetch HN item - temporary error")
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
	if !ircregistry.IsOnChannel(h.port, h.cfg.TopChannel) {
		log.Errorf("cannot send new item: not on channel %q", h.cfg.TopChannel)
		return fmt.Errorf("not on channel")
	}

	item, err := h.getItem(itemNo)
	if err != nil {
		if err2, ok := err.(*hnapi.FetchError); ok && !err2.Temporary() {
			log.Errore(err, "failed to fetch HN item - non-temporary error, ignoring this item")
			return nil
		}
		log.Errore(err, "failed to fetch HN item - prolonged possibly temporary error - ignoring this item anyway")
		return nil
	}

	log.Debugf("%v %+v", itemNo, item)
	h.auditItem(item)
	h.setMostRecentProcessed(itemNo)

	return nil
}

func (h *handler) getItem(itemNo int) (*hnapi.Item, error) {
	for i := 0; i < 5; i++ {
		item, err := h.c.GetItem(itemNo)
		if err != nil {
			log.Errore(err, "failed to fetch HN item")
			return nil, err
		}

		if item.ID != 0 {
			return item, nil
		}

		log.Warne(err, "HN item not present when fetched, retrying", itemNo, i)
		time.Sleep(1 * time.Second)
	}

	return nil, fmt.Errorf("failed to retrieve HN item after multiple attempts: %v", itemNo)
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

func (h *handler) setMostRecentProcessed(itemNo int) {
	h.mostRecentProcessedMutex.Lock()
	defer h.mostRecentProcessedMutex.Unlock()
	if h.mostRecentProcessed > itemNo {
		panic("trying to decrease most recent processed item number - this should not be possible")
	}

	h.mostRecentProcessed = itemNo
	select {
	case h.mostRecentProcessedChan <- struct{}{}:
	default:
	}
}

func (h *handler) getMostRecentProcessed() int {
	h.mostRecentProcessedMutex.Lock()
	defer h.mostRecentProcessedMutex.Unlock()
	return h.mostRecentProcessed
}

func (h *handler) saveLoop() {
	var oldMRP int
	for {
		select {
		case <-h.mostRecentProcessedChan:
			newMRP := h.getMostRecentProcessed()
			if newMRP != oldMRP {
				oldMRP = newMRP
				h.save(newMRP)
				time.Sleep(5 * time.Second)
			}
		}
	}
}

func (h *handler) save(mrp int) {
	log.Debugf("saving mrp: %v", mrp)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(mrp))
	err := h.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("settings"))
		return b.Put([]byte("mostRecentProcessed"), buf[:])
	})
	log.Errore(err, "failed to save database")
}

func (h *handler) load() (int, error) {
	var v int
	err := h.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("settings"))
		buf := b.Get([]byte("mostRecentProcessed"))
		if buf != nil {
			v = int(binary.BigEndian.Uint64(buf))
		}
		return nil
	})
	return v, err
}

// Module configuration.
type Config struct {
	API        hnapi.Config // HN API configuration.
	TopChannel string       // Channel to announce top items in.
	LimitItems int          // Number of items on top list to announce (default 30).

	// Regular expression to match against items for notification purposes. If
	// non-empty, any item with matching title, text or URL results in a
	// notification to the specified nick. Any descendant of any such post will
	// also result in a notification.
	NotifyPattern string

	// Notify on any post by a given HN user. Specify the HN username (case
	// sensitive). If non-empty, any item posted by this user, or any item which
	// is a descendant of such a post, results in a notification to the specified
	// nick. This is independent of NotifyPattern.
	NotifyUser string

	// IRC nickname to send notifications to.
	NotifyNick string

	// If set to a non-zero value, this is the item number at which processing
	// would start. Setting this to 1 would process all HN items ever posted (not
	// recommended). If left as zero, the bot will start from whatever the most
	// recent item is at the time it is first started. This is only used when the
	// database is first created and is ignored if the database exists with a
	// stored most recently processed item number from a previous session.
	InitialMostRecentProcessed int

	// Path to database file used to note most recently processed item.
	DBPath string
}

func Info(cfg *Config) *ircregistry.HandlerInfo {
	return &ircregistry.HandlerInfo{
		Name:        "hntail",
		Description: "HN tailing module",
		NewFunc: func(port ircregistry.Port) (ircregistry.Handler, error) {
			h := &handler{
				port:                    port,
				cfg:                     *cfg,
				watchingParents:         map[int]time.Time{},
				mostRecentProcessedChan: make(chan struct{}, 1),
				mostRecentProcessed:     cfg.InitialMostRecentProcessed,
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

			if h.cfg.DBPath == "" {
				return nil, fmt.Errorf("must specify database path")
			}

			h.db, err = bolt.Open(h.cfg.DBPath, 0644, nil)
			if err != nil {
				return nil, err
			}

			err = h.db.Update(func(tx *bolt.Tx) error {
				_, err := tx.CreateBucketIfNotExists([]byte("settings"))
				return err
			})
			if err != nil {
				return nil, err
			}

			mostRecentProcessed, err := h.load()
			if err != nil {
				return nil, err
			}
			if mostRecentProcessed != 0 {
				h.mostRecentProcessed = mostRecentProcessed
				log.Debugf("resuming from most recent processed item %v", mostRecentProcessed)
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
				MostRecentProcessedItemNo: h.mostRecentProcessed,
			})
			if err != nil {
				return nil, err
			}

			go h.cleanLoop()
			go h.saveLoop()

			return h, nil
		},
	}
}
