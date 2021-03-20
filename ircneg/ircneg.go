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

// Configuration settings for the IRC connection negotiation process.
type Config struct {
	IRCNick         []string             // IRC nicknames in order of preference.
	IRCNickFunc     func(idx int) string // Called to generate nicks if IRCNick list is exhausted. Optional.
	IRCUser         string               // IRC username.
	IRCReal         string               // IRC realname.
	IRCPass         string               // IRC server password. This is not an account password.
	SASLCredentials ircsasl.Credentials  // Leave empty to avoid using SASL.

	// Do not attempt to use IRCv3 capabilities negotiation with the server.
	// SASLCredentials will be ignored if set.
	InhibitCapSupport bool

	// List of capabilities which should be requested if they are supported by
	// the server.
	DesiredCaps []string
}

// Results of an IRC negotiation process.
type Result struct {
	ChosenNick    string            // The nick that was ultimately negotiated.
	CapsAvailable []string          // The capabilities which the server can support.
	CapsEnabled   []string          // The capabilities which were actually enabled.
	CapValues     map[string]string // Caps with values (e.g. sasl=EXTERNAL,PLAIN) list the values here.
	ReadMsgs      []*ircparse.Msg   // All messages which were read during the negotiation.
}

func generateNick(cfg *Config, idx int) string {
	if idx < len(cfg.IRCNick) {
		return cfg.IRCNick[idx]
	}

	var s string
	if cfg.IRCNickFunc != nil {
		s = cfg.IRCNickFunc(idx - len(cfg.IRCNick))
	}

	if s != "" {
		return s
	}

	return fmt.Sprintf("Guest%d", rand.Intn(99999))
}

// Negotiation state.
type negState int

const (
	// We sent CAP LS and are waiting for the server to send the first or
	// additional lines of the cap list.
	nsWaitCapList negState = iota
	// We sent CAP REQ and are waiting for the server to send ACKs or NAKs for
	// each cap we requested.
	nsWaitCapAck
	// Starts SASL and transitions immediately to nsWaitSaslStart.
	nsStartSasl
	// We have submitted SASL commands and are waiting for further SASL stimulus,
	// authentication success or failure.
	nsWaitSasl
	// Sends CAP END and transitions immediately to nsWait001.
	nsEndCap
	// We're done with caps, SASL, etc. or caps were disabled. Now we just wait for 001.
	nsWait001
)

// Given a freshly created IRC connection, perform the negotiation process.
//
// If successful, returns a Result with information about the negotiated parameters.
// The connection can then be used to join channels, etc.
//
// If this function returns failure, the Conn is spent and must be disposed of.
func Negotiate(ctx context.Context, conn ircconn.Abstract, cfg *Config) (res *Result, err error) {
	if len(cfg.IRCUser) == 0 {
		err = fmt.Errorf("must specify a username")
		return
	}

	var allReadMsgs []*ircparse.Msg
	readMsg := func() (*ircparse.Msg, error) {
		msg, err := conn.ReadMsg(ctx)
		if err == nil {
			allReadMsgs = append(allReadMsgs, msg)
		}
		return msg, err
	}

	writeMsg := func(msg *ircparse.Msg) error {
		return conn.WriteMsg(ctx, msg)
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

	// First, send the commands which can be sent immediately.
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

	// Variables for our state machine.
	const (
		dcDesired byte = iota
		dcRequested
		dcGranted
		dcDenied
	)

	desiredCaps := map[string]byte{}
	capValues := map[string]string{}
	var serverSupportedCapsList []string
	var saslEngine *ircsasl.Engine

	// Request all the caps specified in the config.
	for _, dc := range cfg.DesiredCaps {
		desiredCaps[dc] = dcDesired
	}

	// If we want to authenticate with SASL, make sure we're requesting the cap.
	if cfg.SASLCredentials.Username != "" {
		desiredCaps["sasl"] = dcDesired
	}

	// Now we enter our state machine and wait for messages.
	state := nsWaitCapList
	if cfg.InhibitCapSupport {
		state = nsWait001
	}

	var msg *ircparse.Msg
	for {
		switch state {
		case nsStartSasl, nsEndCap:
			msg.Command = ""

		default:
			msg, err = readMsg()
			if err != nil {
				return
			}
		}

		if msg.Command == "001" {
			// 001 Welcome, we're done negotiating.
			// We handle this as an end of negotiation regardless of our state.
			break
		} else if msg.Command == "432" || msg.Command == "433" || msg.Command == "437" {
			// 432 Erroneous nickname
			// 433 Nickname in use
			// 437 Nickname is temporarily unavailable
			// We handle these independently of our negotiation state because we can.
			// It effectively allows us to parallelise nickname conflict resolution
			// and have it occur concurrently with e.g. cap negotiation, SASL, etc.
			// thereby reducing negotiation time where nick conflicts occur.
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

		switch state {

		// We are waiting for the server to list, or continue listing, its supported caps.
		case nsWaitCapList:
			// A valid CAP LS response from the server
			if msg.Command == "CAP" && len(msg.Args) >= 2 && msg.Args[1] == "LS" &&
				(len(msg.Args) == 3 || (len(msg.Args) == 4 && msg.Args[2] == "*")) {
				caps := strings.Split(strings.Trim(msg.Args[len(msg.Args)-1], " "), " ")
				for _, capStr := range caps {
					capParts := strings.SplitN(capStr, "=", 2)
					capName := capParts[0]
					if len(capParts) > 1 {
						capValues[capName] = capParts[1]
					}
					serverSupportedCapsList = append(serverSupportedCapsList, capName)
				}

				if len(msg.Args) != 4 {
					// We have a complete cap list, send our requests and then start waiting for ACKs/NAKs.
					s := ""
					maxLen := 192
					for _, capName := range serverSupportedCapsList {
						if dc, ok := desiredCaps[capName]; ok && dc == dcDesired {
							// This cap is supported, and we desire it, and we have not yet requested it

							// We have accumulated enough cap names in our buffer already that we should
							// flush them as a CAP REQ message before continuing to avoid generating
							// a CAP REQ message which is too long.
							if s != "" && len(s)+len(capName)+1 >= maxLen+1 {
								err = writeMsg(&ircparse.Msg{
									Command: "CAP",
									Args:    []string{"REQ", s[1:]},
								})
								if err != nil {
									return
								}
								s = ""
							}

							s += " " + capName
							desiredCaps[capName] = dcRequested
						}
					}

					// Request any other caps we have yet to request.
					if s != "" {
						err = writeMsg(&ircparse.Msg{
							Command: "CAP",
							Args:    []string{"REQ", s[1:]},
						})
						if err != nil {
							return
						}
					}

					// Done requesting caps, now we wait for the ACK/NAK responses.
					state = nsWaitCapAck
				}
			}

		// We have CAP REQ'd all the caps we want and are now waiting for the server to ACK or NAK every cap we sent.
		case nsWaitCapAck:
			if msg.Command == "CAP" && len(msg.Args) == 3 && (msg.Args[1] == "ACK" || msg.Args[1] == "NAK") {
				isAck := msg.Args[1] == "ACK"
				capStrs := strings.Split(strings.Trim(msg.Args[2], " "), " ")
				for _, capStr := range capStrs {
					// Ensure no value is specified.
					capParts := strings.SplitN(capStr, "=", 2)
					if len(capParts) > 1 {
						err = fmt.Errorf("unexpected cap value in ACK/NAK response")
						return
					}
					capName := capParts[0]

					// Ensure the response is for a cap we requested.
					_, ok := desiredCaps[capName]
					if !ok {
						err = fmt.Errorf("server cap response did not correspond to any request: %q", capName)
						return
					}

					if isAck {
						desiredCaps[capName] = dcGranted
					} else {
						desiredCaps[capName] = dcDenied
					}
				}

				// Check whether any caps are still pending after having processed this command.
				anyPending := false
				for _, dc := range desiredCaps {
					if dc == dcRequested {
						anyPending = true
						break
					}
				}

				// No caps are still pending, we can continue.
				if !anyPending {
					if dc, ok := desiredCaps["sasl"]; ok && dc == dcGranted {
						// Begin SASL.
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

						state = nsStartSasl
					} else {
						// No SASL, we can now CAP END.
						state = nsEndCap
					}
				}
			}

		case nsStartSasl:
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

			state = nsWaitSasl

		// We have started a SASL method and are waiting for the server's initial stimulus.
		case nsWaitSasl:
			if msg.Command == "AUTHENTICATE" {
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

				state = nsWaitSasl
			} else if msg.Command == "903" {
				// 903 SASL authentication successful, we can CAP END now
				state = nsEndCap
			} else if msg.Command == "904" || msg.Command == "908" {
				// 904 SASL authentication failed
				// 908 list of available SASL mechanisms
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

				// Try next method.
				state = nsStartSasl
			}

		case nsEndCap:
			err = writeMsg(&ircparse.Msg{
				Command: "CAP",
				Args:    []string{"END"},
			})
			if err != nil {
				return
			}

			state = nsWait001

		case nsWait001:
			break

		default:
			panic("?")
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
		CapsAvailable: serverSupportedCapsList,
		CapsEnabled:   capsEnabledList,
		CapValues:     capValues,
		ReadMsgs:      allReadMsgs,
	}

	err = nil
	return
}
