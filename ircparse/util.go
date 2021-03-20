package ircparse

import (
	"regexp"
	"strings"
)

func NickMatch(x, y string) bool {
	return strings.EqualFold(x, y)
}

func NormalizeNickName(nickName string) string {
	return strings.ToLower(nickName)
}

func NormalizeChannelName(chanName string) string {
	return strings.ToLower(chanName)
}

var reChannelName = regexp.MustCompile(`^[^,0-9 \r\n][^, \r\n]{0,128}$`)

func IsValidChannelName(chanName string) bool {
	return reChannelName.MatchString(chanName)
}
