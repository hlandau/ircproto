// Package ircparse provides an IRC message parser and serializer supporting RFC1459 and IRCv3 message tags.
package ircparse

import (
	"errors"
	"regexp"
	"strings"
)

// An RFC1459 IRC protocol message. Supports IRCv3 tags.
type Msg struct {
	// Where :server.name is present, this is "server.name".
	ServerName string

	// Where :nick!user@host is present, this is nick, user and host.
	NickName, UserName, HostName string

	// Command name (uppercase) or three-digit numeric.
	Command string

	// All arguments including trailing argument. Note that the trailing argument
	// is not in any way semantically distinct from the other arguments, but it
	// may contain spaces.
	Args []string

	// IRCv3 tags. nil if no tags were specified. Tags without values have empty string values.
	// @tag1=tagvalue;tag2=tagvalue
	Tags map[string]string

	// The raw message which was parsed.
	Raw string
}

// Marshal a message as an RFC1459 protocol line, including any IRCv3 message tags if specified.
func (m *Msg) String() (string, error) {
	if m.Command == "" {
		return m.Raw, nil
	}

	s := ""
	if len(m.Tags) > 0 {
		s += "@"
		first := true
		for k, v := range m.Tags {
			if first {
				first = false
			} else {
				s += ";"
			}
			s += k
			if len(v) > 0 {
				s += "="
				s += v
			}
		}
		s += " "
	}

	if m.ServerName != "" {
		s += ":"
		s += m.ServerName
		s += " "
	} else if m.NickName != "" {
		s += ":"
		s += m.NickName
		s += "!"
		s += m.UserName
		s += "@"
		s += m.HostName
		s += " "
	}

	s += m.Command

	if len(m.Args) > 0 {
		a := m.Args[0 : len(m.Args)-1]
		for _, v := range a {
			s += " "
			s += v
		}

		ta := m.Args[len(m.Args)-1]
		s += " :"
		s += ta
	}

	s += "\r\n"
	return s, nil
}

const reHostOrIP = `[^!@; \r\n]+`
const reUser = `(?P<nick>[^! \r\n]+)!(?P<user>[^@ \r\n]+)@(?P<host>` + reHostOrIP + `)`
const reTags = `(?:(?P<tags>@[^ \r\n]+) )?`
const reFrom = `(?::(?:(?P<server>` + reHostOrIP + `)|` + reUser + `) )?`
const reCmd = `(?P<cmd>[0-9]{3}|[a-zA-Z][^ \r\n]+)`
const reArgs = `(?P<args>(?: [^: \r\n][^ \r\n]*)*)`
const reFinalArgs = `(?: (?P<finalArg>:[^\r\n]*))?`

func mustIdx(re *regexp.Regexp, s string) int {
	idx := re.SubexpIndex(s)
	if idx < 0 {
		panic("invalid subexp name")
	}
	return idx
}

var reParse = regexp.MustCompile("^" + reTags + reFrom + reCmd + reArgs + reFinalArgs + "\r?\n$")
var (
	reIdxHost     = mustIdx(reParse, "host")
	reIdxServer   = mustIdx(reParse, "server")
	reIdxNick     = mustIdx(reParse, "nick")
	reIdxUser     = mustIdx(reParse, "user")
	reIdxTags     = mustIdx(reParse, "tags")
	reIdxCmd      = mustIdx(reParse, "cmd")
	reIdxArgs     = mustIdx(reParse, "args")
	reIdxFinalArg = mustIdx(reParse, "finalArg")
)

var reTag = regexp.MustCompile(`^(?P<key>\+?(?:[^!@; \r\n]+/)?[a-zA-Z0-9-]+)(?:=(?P<value>[^ \r\n;]*))?$`)
var (
	reIdxKey   = mustIdx(reTag, "key")
	reIdxValue = mustIdx(reTag, "value")
)

var reSplit = regexp.MustCompile(" +")

var ErrMalformed = errors.New("malformed IRC message")

// Parse an IRC protocol line. This should end with "\n".
func Parse(s string) (*Msg, error) {
	ms := reParse.FindStringSubmatch(s)
	if ms == nil {
		return nil, ErrMalformed
	}

	msg := &Msg{}
	msg.Raw = s
	msg.Command = ms[reIdxCmd]

	rawArgs := ms[reIdxArgs]
	if rawArgs != "" {
		rawArgs = rawArgs[1:]
	}
	if rawArgs != "" {
		msg.Args = reSplit.Split(rawArgs, -1)
	}

	rawFinalArg := ms[reIdxFinalArg]
	if rawFinalArg != "" {
		msg.Args = append(msg.Args, rawFinalArg[1:])
	}

	msg.ServerName = ms[reIdxServer]
	msg.NickName = ms[reIdxNick]
	msg.UserName = ms[reIdxUser]
	msg.HostName = ms[reIdxHost]

	rawTags := ms[reIdxTags]
	if rawTags != "" {
		msg.Tags = map[string]string{}

		rawTags = rawTags[1:]
		tags := strings.Split(rawTags, ";")
		for _, tag := range tags {
			tms := reTag.FindStringSubmatch(tag)
			if tms == nil {
				return nil, ErrMalformed
			}

			tagK := tms[reIdxKey]
			tagV := tms[reIdxValue]
			msg.Tags[tagK] = tagV
		}
	}

	return msg, nil
}
