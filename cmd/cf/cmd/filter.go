package cmd

import (
	"strings"

	"github.com/campfire-net/campfire/pkg/store"
)

// filterMessages applies post-query filters from a MessageFilter to a slice of messages.
// Include filters (Tags, TagPrefixes) use OR semantics: a message matches if any tag
// matches any exact tag or starts with any prefix.
// Exclude filters (ExcludeTags, ExcludeTagPrefixes) remove messages with matching tags.
// Sender matches on prefix of the sender hex string (case-insensitive).
// If all filter fields are empty, all messages are returned unmodified.
func filterMessages(msgs []store.MessageRecord, mf store.MessageFilter) []store.MessageRecord {
	if len(mf.Tags) == 0 && len(mf.TagPrefixes) == 0 && len(mf.ExcludeTags) == 0 && len(mf.ExcludeTagPrefixes) == 0 && mf.Sender == "" {
		return msgs
	}

	includeSet := make(map[string]bool, len(mf.Tags))
	for _, t := range mf.Tags {
		includeSet[strings.ToLower(t)] = true
	}
	excludeSet := make(map[string]bool, len(mf.ExcludeTags))
	for _, t := range mf.ExcludeTags {
		excludeSet[strings.ToLower(t)] = true
	}

	senderPrefix := strings.ToLower(mf.Sender)

	var result []store.MessageRecord
	for _, m := range msgs {
		if !matchesSender(m.Sender, senderPrefix) {
			continue
		}
		if (len(includeSet) > 0 || len(mf.TagPrefixes) > 0) && !matchesTagsOrPrefixes(m.Tags, includeSet, mf.TagPrefixes) {
			continue
		}
		if matchesExclude(m.Tags, excludeSet, mf.ExcludeTagPrefixes) {
			continue
		}
		result = append(result, m)
	}
	return result
}

// matchesTagsOrPrefixes returns true if any tag matches an exact tag in the set
// or starts with any prefix.
func matchesTagsOrPrefixes(tags []string, tagSet map[string]bool, prefixes []string) bool {
	for _, t := range tags {
		tl := strings.ToLower(t)
		if tagSet[tl] {
			return true
		}
		for _, p := range prefixes {
			if strings.HasPrefix(tl, strings.ToLower(p)) {
				return true
			}
		}
	}
	return false
}

// matchesExclude returns true if any tag matches an excluded exact tag or prefix.
func matchesExclude(tags []string, excludeSet map[string]bool, excludePrefixes []string) bool {
	for _, t := range tags {
		tl := strings.ToLower(t)
		if excludeSet[tl] {
			return true
		}
		for _, p := range excludePrefixes {
			if strings.HasPrefix(tl, strings.ToLower(p)) {
				return true
			}
		}
	}
	return false
}

// matchesSender returns true if the sender hex string starts with the given prefix.
// Empty prefix matches all senders.
func matchesSender(sender, prefix string) bool {
	if prefix == "" {
		return true
	}
	return strings.HasPrefix(strings.ToLower(sender), prefix)
}
