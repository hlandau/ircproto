package hnapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
)

type Item struct {
	Type string `json:"type"`
	ID   int    `json:"id"`
	Time int    `json:"time"`

	// story
	Title       string `json:"title"`
	By          string `json:"by"`
	Score       int    `json:"score"`
	URL         string `json:"url"`
	Descendants int    `json:"descendants"`

	// comment
	Text   string `json:"text"`
	Parent int    `json:"parent"`
	Kids   []int  `json:"kids"`
}

type Config struct {
	HTTPClient *http.Client
	URL        string
}

type Client struct {
	cfg Config
	url *url.URL

	topMutex         sync.Mutex
	topEventChan     chan *Event
	topEventListener *SSEListener
	maxMutex         sync.Mutex
	maxEventChan     chan *Event
	maxEventListener *SSEListener
}

func New(cfg *Config) (*Client, error) {
	c := &Client{
		cfg: *cfg,
	}

	if c.cfg.URL == "" {
		return nil, fmt.Errorf("must specify HN API URL")
	}

	var err error
	c.url, err = url.Parse(c.cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("cannot parse HN API URL: %v", err)
	}

	if c.cfg.HTTPClient == nil {
		c.cfg.HTTPClient = &http.Client{}
	}

	return c, nil
}

func (c *Client) RelativeURL(relURL string) (string, error) {
	u, err := c.url.Parse(relURL)
	if err != nil {
		return "", err
	}

	return u.String(), nil
}

func (c *Client) GetItem(itemNo int) (*Item, error) {
	itemURL, err := c.RelativeURL(fmt.Sprintf("item/%d.json", itemNo))
	if err != nil {
		return nil, err
	}

	res, err := c.cfg.HTTPClient.Get(itemURL)
	if err != nil {
		return nil, err
	}

	var item Item
	defer res.Body.Close()
	err = json.NewDecoder(res.Body).Decode(&item)
	if err != nil {
		return nil, err
	}

	return &item, nil
}

func (c *Client) TopStoryEventChan() (<-chan *Event, error) {
	c.topMutex.Lock()
	defer c.topMutex.Unlock()

	if c.topEventListener == nil {
		topStoriesURL, err := c.RelativeURL("topstories.json")
		if err != nil {
			return nil, err
		}

		c.topEventChan = make(chan *Event)
		c.topEventListener, err = NewSSEListener(topStoriesURL, c.cfg.HTTPClient, c.topEventChan)
		if err != nil {
			return nil, err
		}
	}

	return c.topEventChan, nil
}

func (c *Client) MaxItemEventChan() (<-chan *Event, error) {
	c.maxMutex.Lock()
	defer c.maxMutex.Unlock()

	if c.maxEventListener == nil {
		maxItemURL, err := c.RelativeURL("maxitem.json")
		if err != nil {
			return nil, err
		}

		c.maxEventChan = make(chan *Event)
		c.maxEventListener, err = NewSSEListener(maxItemURL, c.cfg.HTTPClient, c.maxEventChan)
		if err != nil {
			return nil, err
		}
	}

	return c.maxEventChan, nil
}

func UnmarshalTopEvent(ev *Event) ([]int, error) {
	if ev.Type != "put" {
		return nil, nil
	}

	var d struct {
		Data []int `json:"data"`
	}
	err := json.Unmarshal([]byte(ev.Data), &d)
	if err != nil {
		return nil, err
	}

	return d.Data, nil
}

func UnmarshalMaxEvent(ev *Event) (int, error) {
	if ev.Type != "put" {
		return 0, nil
	}

	var d struct {
		Data int `json:"data"`
	}
	err := json.Unmarshal([]byte(ev.Data), &d)
	if err != nil {
		return 0, err
	}

	return d.Data, nil
}
