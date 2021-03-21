package ircparse

import (
	"regexp"
	"strings"
)

// Returns true iff nicknames x and y are the same.
func NickMatch(x, y string) bool {
	return strings.EqualFold(x, y)
}

// Normalizes a nickname by converting it to lowercase.
func NormalizeNickName(nickName string) string {
	return strings.ToLower(nickName)
}

// Normalizes a channel name by converting it to lowercase.
func NormalizeChannelName(chanName string) string {
	return strings.ToLower(chanName)
}

var reChannelName = regexp.MustCompile(`^[^,0-9 \r\n][^, \r\n]{0,128}$`)

// Returns true iff the given string is a valid channel name.
func IsValidChannelName(chanName string) bool {
	return reChannelName.MatchString(chanName)
}
