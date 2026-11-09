package mqttwire

import (
	"errors"
	"strings"
)

// ValidTopicName reports, as an error, whether name is a topic a message
// may be published to: a string MQTT takes, of a byte at least, without
// the wildcards + and #.
func ValidTopicName(name string) error {
	switch {
	case name == "":
		return errors.New("mqtt: an empty topic name")
	case !validString(name):
		return errors.New("mqtt: a topic name of more than 65535 bytes, not UTF-8, or with a null character")
	case strings.ContainsAny(name, "+#"):
		return errors.New("mqtt: a topic name with a wildcard, + or #")
	}
	return nil
}

// ValidTopicFilter reports, as an error, whether filter is one a client may
// subscribe to: + standing for a whole level, # for the last level and
// those under it; a shared subscription's $share/group/filter too.
func ValidTopicFilter(filter string) error {
	if filter == "" {
		return errors.New("mqtt: an empty topic filter")
	}
	if !validString(filter) {
		return errors.New("mqtt: a topic filter of more than 65535 bytes, not UTF-8, or with a null character")
	}
	if rest, ok := strings.CutPrefix(filter, "$share/"); ok {
		group, f, ok := strings.Cut(rest, "/")
		if !ok || group == "" || strings.ContainsAny(group, "+#") || f == "" {
			return errors.New("mqtt: a shared subscription is $share/group/filter")
		}
		filter = f
	}
	levels := strings.Split(filter, "/")
	for i, l := range levels {
		switch {
		case l == "#" && i != len(levels)-1:
			return errors.New("mqtt: # is not the topic filter's last level")
		case len(l) > 1 && strings.ContainsAny(l, "+#"):
			return errors.New("mqtt: a wildcard, + or #, is not a whole level of the topic filter")
		}
	}
	return nil
}

// MatchTopic reports whether the topic name matches the filter, as a
// server matches them: a filter starting with a wildcard does not match a
// topic starting with $ ($SYS/...). A shared subscription's filter is
// matched without its $share/group/.
func MatchTopic(filter, topic string) bool {
	if rest, ok := strings.CutPrefix(filter, "$share/"); ok {
		if _, f, ok := strings.Cut(rest, "/"); ok {
			filter = f
		}
	}
	if strings.HasPrefix(topic, "$") && (strings.HasPrefix(filter, "+") || strings.HasPrefix(filter, "#")) {
		return false
	}
	fs, ts := strings.Split(filter, "/"), strings.Split(topic, "/")
	for i, f := range fs {
		if f == "#" {
			return true
		}
		if i >= len(ts) || f != "+" && f != ts[i] {
			return false
		}
	}
	return len(fs) == len(ts)
}
