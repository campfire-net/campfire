package cmd

import (
	"strings"

	"github.com/campfire-net/campfire/pkg/store"
)

// filterMessages applies post-query tag and sender filters to a slice of messages.
// tagFilters uses OR semantics: a message matches if it has ANY of the specified tags.
// senderFilter matches on prefix of the sender hex string (case-insensitive).
// If both filters are empty, all messages are returned unmodified.
func filterMessages(msgs []store.MessageRecord, tagFilters []string, senderFilter string) []store.MessageRecord {
	if len(tagFilters) == 0 && senderFilter == "" {
		return msgs
	}

	// Build a set for fast tag lookup.
	tagSet := make(map[string]bool, len(tagFilters))
	for _, t := range tagFilters {
		tagSet[strings.ToLower(t)] = true
	}

	senderPrefix := strings.ToLower(senderFilter)

	var result []store.MessageRecord
	for _, m := range msgs {
		if !matchesSender(m.Sender, senderPrefix) {
			continue
		}
		if len(tagSet) > 0 && !matchesTags(m.Tags, tagSet) {
			continue
		}
		result = append(result, m)
	}
	return result
}

// matchesTags returns true if the tags slice contains any tag in the set.
// Tags are now typed []string on MessageRecord (JSON deserialization happens at the store boundary).
func matchesTags(tags []string, tagSet map[string]bool) bool {
	for _, t := range tags {
		if tagSet[strings.ToLower(t)] {
			return true
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
