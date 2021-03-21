package hntail

import (
	"github.com/hlandau/ircproto/egobot/hnapi"
	"github.com/hlandau/xlog"
	"time"
)

var log, Log = xlog.New("hntail")

type Config struct {
	Client                    *hnapi.Client
	OnNewTopItem              func(itemNo int) error
	OnNewItem                 func(itemNo int) error
	LimitItems                int
	MostRecentProcessedItemNo int
}

type Tailer struct {
	cfg     Config
	topChan <-chan *hnapi.Event
	maxChan <-chan *hnapi.Event
}

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
					log.Errore(err, "failed processing HN new item ID %v", itemNo)
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
			if now.Sub(r.LastSeen) > 10*time.Minute {
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
				log.Errore(err, "failed processing HN max item ID %v", lastItemNo)
				break
			}
		}
	}
}
