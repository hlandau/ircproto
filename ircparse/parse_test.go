package ircparse

import (
	"reflect"
	"testing"
)

type item struct {
	s, rto string
	ok     bool
	m      Msg
}

var ss = []item{
	item{"PING\r\n", "", true, Msg{
		Command: "PING",
	}},
	item{"PING foo\r\n", "PING :foo\r\n", true, Msg{
		Command: "PING",
		Args:    []string{"foo"},
	}},
	item{"PING foo bar\r\n", "PING foo :bar\r\n", true, Msg{
		Command: "PING",
		Args:    []string{"foo", "bar"},
	}},
	item{"PING foo bar baz\r\n", "PING foo bar :baz\r\n", true, Msg{
		Command: "PING",
		Args:    []string{"foo", "bar", "baz"},
	}},
	item{"PING foo bar baz :alpha beta gamma delta\r\n", "", true, Msg{
		Command: "PING",
		Args:    []string{"foo", "bar", "baz", "alpha beta gamma delta"},
	}},
	item{"043 okay well :this is a numeric\r\n", "", true, Msg{
		Command: "043",
		Args:    []string{"okay", "well", "this is a numeric"},
	}},
	item{"01 bad numeric\r\n", "", false, Msg{}},
	item{"0199 bad numeric\r\n", "", false, Msg{}},
	item{"\r\n", "", false, Msg{}},
	item{" \r\n", "", false, Msg{}},
	item{"  \r\n", "", false, Msg{}},
	item{":server PING\r\n", "", true, Msg{
		ServerName: "server",
		Command:    "PING",
	}},
	item{":server.name PING\r\n", "", true, Msg{
		ServerName: "server.name",
		Command:    "PING",
	}},
	item{":the.server-name PING\r\n", "", true, Msg{
		ServerName: "the.server-name",
		Command:    "PING",
	}},
	item{":the.server-name. PING\r\n", "", true, Msg{
		ServerName: "the.server-name.",
		Command:    "PING",
	}},
	item{":127.0.0.1 PING\r\n", "", true, Msg{
		ServerName: "127.0.0.1",
		Command:    "PING",
	}},
	//item{":127.0.0.1. PING\r\n", false, Msg{}},
	item{":::1 PING\r\n", "", true, Msg{
		ServerName: "::1",
		Command:    "PING",
	}},
	item{"::: PING\r\n", "", true, Msg{
		ServerName: "::",
		Command:    "PING",
	}},
	item{":abcd:dabc:cdab:bcda:dcba:adcb:badc:cbad PING\r\n", "", true, Msg{
		ServerName: "abcd:dabc:cdab:bcda:dcba:adcb:badc:cbad",
		Command:    "PING",
	}},
	item{":ABCD:DABC:CDAB:BCDA:DCBA:ADCB:BADC:CBAD PING\r\n", "", true, Msg{
		ServerName: "ABCD:DABC:CDAB:BCDA:DCBA:ADCB:BADC:CBAD",
		Command:    "PING",
	}},
	item{":1234:4567:8901:2345:6789:0123:4567:8901 PING\r\n", "", true, Msg{
		ServerName: "1234:4567:8901:2345:6789:0123:4567:8901",
		Command:    "PING",
	}},
	item{":2001::1 PING\r\n", "", true, Msg{
		ServerName: "2001::1",
		Command:    "PING",
	}},
	item{":2001:1234::1234:1 PING\r\n", "", true, Msg{
		ServerName: "2001:1234::1234:1",
		Command:    "PING",
	}},
	//item{":2001::1::1 PING\r\n", false, Msg{}},
	item{":FFfF::BeEF:255.255.255.255 PING\r\n", "", true, Msg{
		ServerName: "FFfF::BeEF:255.255.255.255",
		Command:    "PING",
	}},
	item{":this/is/a/host PING\r\n", "", true, Msg{
		ServerName: "this/is/a/host",
		Command:    "PING",
	}},
	item{":nick!user@host PING\r\n", "", true, Msg{
		NickName: "nick",
		UserName: "user",
		HostName: "host",
		Command:  "PING",
	}},
	item{":nick!user@host.name PING\r\n", "", true, Msg{
		NickName: "nick",
		UserName: "user",
		HostName: "host.name",
		Command:  "PING",
	}},
	item{":nick!user@111.222.101.104 PING\r\n", "", true, Msg{
		NickName: "nick",
		UserName: "user",
		HostName: "111.222.101.104",
		Command:  "PING",
	}},
	// Charybdis can mangle IP addresses to use non-hexadecimal letters; we must accept this
	item{":nick!user@111.222.xyz.xWz PING\r\n", "", true, Msg{
		NickName: "nick",
		UserName: "user",
		HostName: "111.222.xyz.xWz",
		Command:  "PING",
	}},
	item{":nick!user@dead:beef:dead:beef:1234:4567:89ad:beef PING\r\n", "", true, Msg{
		NickName: "nick",
		UserName: "user",
		HostName: "dead:beef:dead:beef:1234:4567:89ad:beef",
		Command:  "PING",
	}},
	// Charybdis can mangle IP addresses to use non-hexadecimal letters; we must accept this
	item{":nick!user@dead:bxyz:dXYZ:beef:1234:z567:89ad:Zeef PING\r\n", "", true, Msg{
		NickName: "nick",
		UserName: "user",
		HostName: "dead:bxyz:dXYZ:beef:1234:z567:89ad:Zeef",
		Command:  "PING",
	}},
	item{":nick!user@zead:bxyz:dXYZ:beef:1234:z567:89ad:Zeef PING\r\n", "", true, Msg{
		NickName: "nick",
		UserName: "user",
		HostName: "zead:bxyz:dXYZ:beef:1234:z567:89ad:Zeef",
		Command:  "PING",
	}},
	// Freenode can use slashes in vhosts
	item{":nick!user@this/is/my/Host PING\r\n", "", true, Msg{
		NickName: "nick",
		UserName: "user",
		HostName: "this/is/my/Host",
		Command:  "PING",
	}},
	item{":nick!~user@host.name PING\r\n", "", true, Msg{
		NickName: "nick",
		UserName: "~user",
		HostName: "host.name",
		Command:  "PING",
	}},
	item{"@alpha :nick!~user@host.name PING foo bar :this is message\r\n", "", true, Msg{
		NickName: "nick",
		UserName: "~user",
		HostName: "host.name",
		Command:  "PING",
		Args:     []string{"foo", "bar", "this is message"},
		Tags: map[string]string{
			"alpha": "",
		},
	}},
	item{"@alpha=beta :nick!~user@host.name PING foo bar :this is message\r\n", "", true, Msg{
		NickName: "nick",
		UserName: "~user",
		HostName: "host.name",
		Command:  "PING",
		Args:     []string{"foo", "bar", "this is message"},
		Tags: map[string]string{
			"alpha": "beta",
		},
	}},
	item{"@example.com/alpha=beta :nick!~user@host.name PING foo bar :this is message\r\n", "", true, Msg{
		NickName: "nick",
		UserName: "~user",
		HostName: "host.name",
		Command:  "PING",
		Args:     []string{"foo", "bar", "this is message"},
		Tags: map[string]string{
			"example.com/alpha": "beta",
		},
	}},
	item{"@baz=1;example.com/alpha=beta;foo=bar :nick!~user@host.name PING foo bar :this is message\r\n", "", true, Msg{
		NickName: "nick",
		UserName: "~user",
		HostName: "host.name",
		Command:  "PING",
		Args:     []string{"foo", "bar", "this is message"},
		Tags: map[string]string{
			"example.com/alpha": "beta",
			"foo":               "bar",
			"baz":               "1",
		},
	}},
	item{"@baz=9\\s\\\\31;example.com/alpha=be\\:ta;foo=b\\r\\nar :nick!~user@host.name PING foo bar :this is message\r\n", "", true, Msg{
		NickName: "nick",
		UserName: "~user",
		HostName: "host.name",
		Command:  "PING",
		Args:     []string{"foo", "bar", "this is message"},
		Tags: map[string]string{
			"example.com/alpha": "be;ta",
			"foo":               "b\r\nar",
			"baz":               "9 \\31",
		},
	}},

	item{":ChanServ!ChanServ@services. MODE ##foo +z \r\n", ":ChanServ!ChanServ@services. MODE ##foo :+z\r\n", true, Msg{
		NickName: "ChanServ",
		UserName: "ChanServ",
		HostName: "services.",
		Command:  "MODE",
		Args:     []string{"##foo", "+z"},
	}},
}

func TestParse(t *testing.T) {
	for _, i := range ss {
		msg, err := Parse(i.s)
		if err == nil && !i.ok {
			t.Errorf("parse was supposed to fail but didn't: %s", i.s)
			continue
		}
		if err != nil && i.ok {
			t.Errorf("parse was supposed to succeed but didn't: %s", i.s)
			continue
		}

		if i.ok {
			msg.Raw = ""
		}

		if i.ok && !reflect.DeepEqual(msg, &i.m) {
			t.Errorf("mismatch:\nfor:       %q\nexpected:  %#v\ngot:       %#v", i.s, &i.m, msg)
			continue
		}

		if msg != nil {
			expect := i.s
			if i.rto != "" {
				expect = i.rto
			}

			s, err := msg.String()
			if err != nil {
				panic(err)
			}
			if s != expect {
				t.Errorf("mismatch between input and output:\ninput:  %q\noutput: %q\n", expect, s)
			}
		}
	}
}
