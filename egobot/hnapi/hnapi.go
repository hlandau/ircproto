// Package hnapi provides basic functions for retrieving data from the Hacker News API.
package hnapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
)

// A Hacker News item.
type Item struct {
	Type string `json:"type"`
	ID   int    `json:"id"`
	Time int    `json:"time"`

	// Not all fields are present for all types of item.
	Title       string `json:"title"`
	By          string `json:"by"`
	Score       int    `json:"score"`
	URL         string `json:"url"`
	Descendants int    `json:"descendants"`
	Text        string `json:"text"`
	Parent      int    `json:"parent"`
	Kids        []int  `json:"kids"`
}

type Config struct {
	HTTPClient    *http.Client // HTTP client to use to make requests, or nil for default.
	HTTPUserAgent string       // If HTTPClient is nil, HTTP user agent string to use.
	URL           string       // Base URL of the Hacker News API. Must end in "/".
}

// A Hacker News API client.
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

// Create a new Hacker News API client.
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
		c.cfg.HTTPClient = &http.Client{
			Transport: &dummyTransport{c.cfg.HTTPUserAgent},
		}
	}

	return c, nil
}

// Transport to set User-Agent.
type dummyTransport struct {
	UserAgent string
}

func (d *dummyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if d.UserAgent != "" {
		req.Header.Set("User-Agent", d.UserAgent)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// Get a Hacker News API URL by resolving the given relative URL relative to
// the configured base URL.
func (c *Client) RelativeURL(relURL string) (string, error) {
	u, err := c.url.Parse(relURL)
	if err != nil {
		return "", err
	}

	return u.String(), nil
}

// Information about an item retrieval error.
type FetchError struct {
	ItemNo     int    // The item that was requested.
	StatusCode int    // HTTP status code.
	Status     string // HTTP status string.
}

func (e *FetchError) Error() string {
	return fmt.Sprintf("error while fetching item %v: %v: %v", e.ItemNo, e.StatusCode, e.Status)
}

// Whether this appears to be a temporary error.
func (e *FetchError) Temporary() bool {
	return e.StatusCode >= 500 && e.StatusCode < 600
}

// Fetch an item with the given ID from the API. May return FetchError or other
// errors.
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
	if res.StatusCode != 200 {
		return nil, &FetchError{itemNo, res.StatusCode, res.Status}
	}

	err = json.NewDecoder(res.Body).Decode(&item)
	if err != nil {
		return nil, err
	}

	return &item, nil
}

// Calling this sets up a top story event listener, which yields events
// whenever the top stories change. Use UnmarshalTopEvent to access the data.
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

// Calling this sets up a max item event listener, which yields events whenever
// the maximum item number changes. Use UnmarshalMaxEvent on the result to
// access the maximum item number.
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

// Unmarshals a top stories set change event. Returns the list of story IDs
// which are on the top list.
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

// Unmarshals a maximum item number change event. Returns the new maximum item
// number.
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
