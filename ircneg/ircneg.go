// Package ircneg provides functions for the initial handshaking of new IRC
// connections.
package ircneg

import (
	"context"
	"fmt"
	"math/rand"
	"strings"

	"github.com/hlandau/ircproto/ircconn"
	"github.com/hlandau/ircproto/ircparse"
	"github.com/hlandau/ircproto/ircsasl"
)

type Config struct {
	IRCNick         []string // IRC nicknames in order of preference.
	IRCUser         string   // IRC username.
	IRCReal         string   // IRC realname.
	IRCPass         string   // IRC server password. This is not an account password.
	SASLCredentials ircsasl.Credentials

	// Do not attempt to use IRCv3 capabilities negotiation with the server.
	// SASLUsername and SASLPassword will be ignored if set.
	InhibitCapSupport bool

	// List of capabilities which should be requested if they are supported by
	// the server.
	DesiredCaps []string

	// Called with any message transmitted by the negotiation process, if set.
	OnWrite func(msg *ircparse.Msg)

	// Called with any message received during the negotiation process, if set.
	OnRead func(msg *ircparse.Msg)
}

type Result struct {
	ChosenNick    string
	CapsAvailable []string
	CapsEnabled   []string
	CapValues     map[string]string
}

func generateNick(cfg *Config, idx int) string {
	if idx < len(cfg.IRCNick) {
		return cfg.IRCNick[idx]
	}

	return fmt.Sprintf("Guest%d", rand.Intn(99999))
}

func Negotiate(ctx context.Context, conn *ircconn.Conn, cfg *Config) (res *Result, err error) {
	var saslEngine *ircsasl.Engine

	if len(cfg.IRCUser) == 0 {
		err = fmt.Errorf("must specify a username")
		return
	}

	if len(cfg.IRCNick) == 0 {
		err = fmt.Errorf("must specify at least one nickname")
		return
	}

	readMsg := func() (*ircparse.Msg, error) {
		msg, err := conn.ReadMsg()
		if err == nil && cfg.OnRead != nil {
			cfg.OnRead(msg)
		}
		return msg, err
	}

	writeMsg := func(msg *ircparse.Msg) error {
		if cfg.OnWrite != nil {
			cfg.OnWrite(msg)
		}
		return conn.WriteMsg(msg)
	}

	// If we return due to e.g. a TX error, ensure we drain any remaining
	// messages sent to us as they might contain useful information (ban details,
	// etc.). We don't do anything with the results but OnRead will be called.
	defer func() {
		if err != nil {
			for {
				_, err2 := readMsg()
				if err2 != nil {
					break
				}
			}
		}
	}()

	if cfg.IRCPass != "" {
		err = writeMsg(&ircparse.Msg{
			Command: "PASS",
			Args:    []string{cfg.IRCPass},
		})
		if err != nil {
			return
		}
	}

	if !cfg.InhibitCapSupport {
		err = writeMsg(&ircparse.Msg{
			Command: "CAP",
			Args:    []string{"LS", "302"},
		})
		if err != nil {
			return
		}
	}

	err = writeMsg(&ircparse.Msg{
		Command: "USER",
		Args:    []string{cfg.IRCUser, "*", "*", cfg.IRCReal},
	})
	if err != nil {
		return
	}

	nickAttempt := 0
	chosenNick := generateNick(cfg, nickAttempt)
	err = writeMsg(&ircparse.Msg{
		Command: "NICK",
		Args:    []string{chosenNick},
	}) // NICK
	if err != nil {
		return
	}

	desiredCaps := map[string]byte{} // 0=desired, 1=requested, 2=granted, 3=denied
	capValues := map[string]string{}
	for _, dc := range cfg.DesiredCaps {
		desiredCaps[dc] = 0
	}
	if cfg.SASLCredentials.Username != "" {
		desiredCaps["sasl"] = 0
	}

	var totalCapsList []string
	waitingForCaps := true
	haveSentCapEnd := false
	waitForSasl := false
	sentSasl := false
	var msg *ircparse.Msg
	for {
		msg, err = readMsg()
		if err != nil {
			return
		}

		if !cfg.InhibitCapSupport && msg.Command == "CAP" && len(msg.Args) >= 2 && msg.Args[0] == "*" && msg.Args[1] == "LS" &&
			(len(msg.Args) == 3 || (len(msg.Args) == 4 && msg.Args[2] == "*")) {
			caps := strings.Split(strings.Trim(msg.Args[len(msg.Args)-1], " "), " ")
			for _, capStr := range caps {
				capParts := strings.SplitN(capStr, "=", 2)
				capName := capParts[0]
				if len(capParts) > 1 {
					capValues[capName] = capParts[1]
				}
				totalCapsList = append(totalCapsList, capName)
			}
			if len(msg.Args) != 4 {
				waitingForCaps = false
			}
		}

		if !cfg.InhibitCapSupport && msg.Command == "CAP" && len(msg.Args) == 3 && msg.Args[0] == "*" && (msg.Args[1] == "ACK" || msg.Args[1] == "NAK") {
			isAck := msg.Args[1] == "ACK"
			capNames := strings.Split(strings.Trim(msg.Args[2], " "), " ")
			for _, capStr := range capNames {
				capParts := strings.SplitN(capStr, "=", 2)
				capName := capParts[0]
				_, ok := desiredCaps[capName]
				if !ok {
					err = fmt.Errorf("server cap response did not correspond to any request: %q", capName)
					return
				}
				if isAck {
					desiredCaps[capName] = 2
					if capName == "sasl" {
						waitForSasl = true
					}
				} else {
					desiredCaps[capName] = 3
				}
			}
		}

		if !cfg.InhibitCapSupport && !waitingForCaps && !haveSentCapEnd {
			sentAny := false
			maxLen := 192
			s := ""
			for _, cap := range totalCapsList {
				if x, ok := desiredCaps[cap]; ok && x == 0 {
					if s != "" && len(s)+len(cap)+1 >= maxLen+1 {
						err = writeMsg(&ircparse.Msg{
							Command: "CAP",
							Args:    []string{"REQ", s[1:]},
						})
						if err != nil {
							return
						}
						s = ""
					}

					s += " " + cap
					sentAny = true
					desiredCaps[cap] = 1
				}
			}
			if s != "" {
				err = writeMsg(&ircparse.Msg{
					Command: "CAP",
					Args:    []string{"REQ", s[1:]},
				})
				if err != nil {
					return
				}
			}

			anyPending := false
			for _, v := range desiredCaps {
				if v == 1 {
					anyPending = true
					break
				}
			}

		nextMethod:
			if waitForSasl && !sentSasl {
				saslMethodsStr, ok := capValues["sasl"]
				if !ok {
					saslMethodsStr = "EXTERNAL,PLAIN"
				}
				if saslEngine == nil {
					saslMethods := strings.Split(saslMethodsStr, ",")
					saslEngine, err = ircsasl.NewEngine(&ircsasl.Config{
						Credentials:      cfg.SASLCredentials,
						AvailableMethods: saslMethods,
					})
					if err != nil {
						return
					}
				}
				var chosenMethod string
				chosenMethod, err = saslEngine.BeginMethod()
				if err != nil {
					return
				}

				err = writeMsg(&ircparse.Msg{
					Command: "AUTHENTICATE",
					Args:    []string{chosenMethod},
				})
				if err != nil {
					return
				}

				sentSasl = true
			} else if msg.Command == "AUTHENTICATE" {
				if len(msg.Args) != 1 {
					err = fmt.Errorf("unexpected number of arguments for authenticate command")
					return
				}
				var saslResponse string
				saslResponse, err = saslEngine.Stimulus(msg.Args[0])
				if err != nil {
					return
				}
				err = writeMsg(&ircparse.Msg{
					Command: "AUTHENTICATE",
					Args:    []string{saslResponse},
				})
				if err != nil {
					return
				}
			} else if msg.Command == "903" {
				waitForSasl = false
			} else if msg.Command == "904" || msg.Command == "908" {
				if msg.Command == "908" {
					if len(msg.Args) < 2 {
						err = fmt.Errorf("unexpected 908 arg count")
						return
					}
					err = saslEngine.SetMethods(strings.Split(msg.Args[1], ","))
					if err != nil {
						return
					}
				}
				sentSasl = false
				goto nextMethod
			}

			if !sentAny && !anyPending && !waitForSasl {
				err = writeMsg(&ircparse.Msg{
					Command: "CAP",
					Args:    []string{"END"},
				})
				if err != nil {
					return
				}
				haveSentCapEnd = true
			}
		}

		if msg.Command == "432" || msg.Command == "433" {
			// 433 Nickname in use
			// 432 Erroneous nickname
			nickAttempt++
			chosenNick = generateNick(cfg, nickAttempt)
			err = writeMsg(&ircparse.Msg{
				Command: "NICK",
				Args:    []string{chosenNick},
			})
			if err != nil {
				return
			}
		}

		if msg.Command == "001" {
			// 001 welcome, we're done negotiating.
			break
		}
	}

	var capsEnabledList []string
	for _, cap := range cfg.DesiredCaps {
		if v, ok := desiredCaps[cap]; ok && v == 2 {
			capsEnabledList = append(capsEnabledList, cap)
		}
	}

	res = &Result{
		ChosenNick:    chosenNick,
		CapsAvailable: totalCapsList,
		CapsEnabled:   capsEnabledList,
		CapValues:     capValues,
	}
	err = nil
	return
}
