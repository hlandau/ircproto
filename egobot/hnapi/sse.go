package hnapi

import (
	"bufio"
	unet "github.com/hlandau/goutils/net"
	"io"
	"net/http"
	"strings"
	"sync"
)

type Event struct {
	Type string
	Data string
	ID   string
}

// An HTTP Server Sent Events client.
type SSEListener struct {
	url         string
	client      *http.Client
	notifyChan  chan<- *Event
	closeChan   chan struct{}
	closeOnce   sync.Once
	closer      io.Closer
	closerMutex sync.Mutex
	backoff     unet.Backoff
}

// Create a new SSE listener which connects to the given URL, using the given
// HTTP client (or a default client if nil is specified) and sending
// notifications on notifyChan when events occur. Reconnects automatically if
// the connection is lost.
func NewSSEListener(url string, client *http.Client, notifyChan chan<- *Event) (*SSEListener, error) {
	l := &SSEListener{
		url:        url,
		client:     client,
		notifyChan: notifyChan,
		closeChan:  make(chan struct{}),
	}

	if l.client == nil {
		l.client = &http.Client{}
	}

	res, err := l.open()
	if err != nil {
		return nil, err
	}

	go l.loop(res)
	return l, nil
}

// Tears down the listener.
//
// Calling this multiple times is inconsequential.
func (l *SSEListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closeChan)
		l.closer.Close()
	})
	return nil
}

func (l *SSEListener) loop(res *http.Response) {
	defer close(l.notifyChan)
	for {
		var res *http.Response
		var err error
		if res == nil {
			select {
			case <-l.closeChan:
				return
			default:
			}
			res, err = l.open()
			if err != nil {
				res = nil
				if !l.backoff.Sleep() {
					panic("...")
				}
				continue
			}
		}

		l.sessionLoop(res)
		res = nil
	}
}

func (l *SSEListener) open() (*http.Response, error) {
	req, err := http.NewRequest("GET", l.url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "text/event-stream")
	res, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}

	l.closerMutex.Lock()
	defer l.closerMutex.Unlock()
	l.closer = res.Body
	return res, nil
}

func (l *SSEListener) sessionLoop(res *http.Response) {
	defer res.Body.Close()
	log.Debugf("SSE stream open")

	rdr := bufio.NewReader(res.Body)
	ev := &Event{}
	for {
		line, err := rdr.ReadString('\n')
		if err != nil {
			log.Debugf("SSE stream errored: %v", err)
			break
		}

		line = strings.TrimRight(line, "\r\n")

		if line == "" {
			l.notifyChan <- ev
			ev = &Event{}
			continue
		}

		if line[0] == ':' {
			// Comment
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) < 2 {
			continue
		}

		k, v := parts[0], parts[1]
		if len(v) > 0 && v[0] == ' ' {
			v = v[1:]
		}

		switch k {
		case "event":
			ev.Type = v

		case "data":
			ev.Data = v
			l.notifyChan <- ev
			l.backoff.Reset()

		case "id":
			ev.ID = v

		case "retry":
		}
	}
}
