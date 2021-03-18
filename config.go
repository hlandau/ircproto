package ircproto

import (
	unet "github.com/hlandau/goutils/net"
	"github.com/hlandau/ircproto/ircconn"
	"github.com/hlandau/ircproto/ircneg"
)

// Configuration for IRC clients.
type Config struct {
	// Should return a valid config. Called every time a new connection is
	// made.
	ConnConfigFunc func() (*ircconn.Config, error)
	// Should return a valid config. Called every time a new connection is made.
	NegConfigFunc func() (*ircneg.Config, error)
	// Return the URLs to connect to. Called every time a new connection is made.
	URLListFunc func() ([]string, error)
	// Backoff timer.
	Backoff unet.Backoff
}

func (cfg *Config) WithConn(ccfg *ircconn.Config) *Config {
	cfg.ConnConfigFunc = func() (*ircconn.Config, error) {
		return ccfg, nil
	}
	return cfg
}

func (cfg *Config) WithNeg(ncfg *ircneg.Config) *Config {
	cfg.NegConfigFunc = func() (*ircneg.Config, error) {
		return ncfg, nil
	}
	return cfg
}

func (cfg *Config) WithURLs(urls ...string) *Config {
	cfg.URLListFunc = func() ([]string, error) {
		return urls, nil
	}
	return cfg
}

func Simple(ncfg *ircneg.Config, urls ...string) *Config {
	cfg := &Config{}
	cfg.WithNeg(ncfg)
	cfg.WithURLs(urls...)
	return cfg
}
