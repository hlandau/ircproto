// Package hntail provides facilities for being alerted when new HN API events
// occur, and consuming those events like tailing a log.
package hntail

import (
	"github.com/hlandau/ircproto/egobot/hnapi"
	"github.com/hlandau/xlog"
	"time"
)

var log, Log = xlog.New("hntail")

// Tailer configuration.
type Config struct {
	// Required. The tailer must be provided with an instantiated HN API client.
	Client *hnapi.Client

	// Called synchronously when there is a new item on the toplist.
	OnNewTopItem func(itemNo int) error

	// Called synchronously when there is any new item globally.
	OnNewItem func(itemNo int) error

	// Limit the number of items checked on the toplist. By default, the toplist
	// gives 500 items, not the 30 as on the HN website front page. It is
	// recommended to set this to a lower value. Default is 30.
	LimitItems int

	// If set to a non-zero value, the tailer starts tailing the new item list
	// from here, rather than at whatever the most recent new item is at the time
	// the tailer is started.
	MostRecentProcessedItemNo int
}

// A Hacker News tailer.
type Tailer struct {
	cfg     Config
	topChan <-chan *hnapi.Event
	maxChan <-chan *hnapi.Event
}

// Create a new tailer which consumes the Hacker News API.
func New(cfg *Config) (*Tailer, error) {
	tailer := &Tailer{
		cfg: *cfg,
	}

	if tailer.cfg.LimitItems == 0 {
		tailer.cfg.LimitItems = 30
	}

	var err error
	tailer.topChan, err = tailer.cfg.Client.TopStoryEventChan()
	if err != nil {
		return nil, err
	}

	tailer.maxChan, err = tailer.cfg.Client.MaxItemEventChan()
	if err != nil {
		return nil, err
	}

	go tailer.monLoop()
	go tailer.mon2Loop()

	return tailer, nil
}

// TODO
func (tailer *Tailer) Destroy() error {
	return nil
}

type monRecord struct {
	LastSeen time.Time
}

func (tailer *Tailer) monLoop() {
	records := map[int]*monRecord{}
	preseedItemCount := 0

	for {
		ev := <-tailer.topChan
		items, err := hnapi.UnmarshalTopEvent(ev)
		if err != nil {
			continue
		}

		now := time.Now()

		if len(items) > tailer.cfg.LimitItems {
			items = items[0:tailer.cfg.LimitItems]
		}

		for _, itemNo := range items {
			if r, ok := records[itemNo]; ok {
				r.LastSeen = now
				continue
			}

			if preseedItemCount >= tailer.cfg.LimitItems {
				err := tailer.cfg.OnNewTopItem(itemNo)
				if err != nil {
					log.Errore(err, "failed processing HN new item ID", itemNo)
					continue
				}
			} else {
				preseedItemCount++
			}

			records[itemNo] = &monRecord{
				LastSeen: now,
			}
		}

		var delQueue []int
		for itemNo, r := range records {
			if now.Sub(r.LastSeen) > 2*time.Hour {
				delQueue = append(delQueue, itemNo)
			}
		}
		for _, itemNo := range delQueue {
			delete(records, itemNo)
		}
	}
}

func (tailer *Tailer) mon2Loop() {
	lastItemNo := tailer.cfg.MostRecentProcessedItemNo

	for {
		ev := <-tailer.maxChan
		maxItemNo, err := hnapi.UnmarshalMaxEvent(ev)
		if err != nil {
			continue
		}

		if lastItemNo == 0 {
			lastItemNo = maxItemNo
		}

		time.Sleep(1 * time.Second)
		for ; lastItemNo < maxItemNo; lastItemNo++ {
			err = tailer.cfg.OnNewItem(lastItemNo)
			if err != nil {
				log.Errore(err, "failed processing HN max item ID", lastItemNo)
				break
			}
		}
	}
}
