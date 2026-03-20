package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// natPollConfig holds all parameters for the NAT poll loop.
type natPollConfig struct {
	campfireID  string
	peers       []store.PeerEndpoint
	cursor      int64
	follow      bool
	id          *identity.Identity
	timeoutSecs int
	// st is used to resolve key display names. May be nil (falls back to unknown://).
	st *store.Store
	// stopCh receives a signal to terminate the loop. If nil, runNATPoll
	// registers its own SIGINT/SIGTERM handler.
	stopCh chan os.Signal
	// tagFilters and senderFilter apply the same --tag/--sender semantics as
	// the direct-mode read path. Empty values mean no filtering.
	tagFilters   []string
	senderFilter string
}

// filterNATMessages applies tag and sender filters to a slice of message.Message.
// tagFilters uses OR semantics: a message matches if it has ANY of the specified tags.
// senderFilter matches on a hex prefix of the sender bytes (case-insensitive).
// Empty values mean no filtering.
// Filtering is delegated to the shared matchesSender and matchesTags helpers in filter.go.
func filterNATMessages(msgs []message.Message, tagFilters []string, senderFilter string) []message.Message {
	if len(tagFilters) == 0 && senderFilter == "" {
		return msgs
	}

	tagSet := make(map[string]bool, len(tagFilters))
	for _, t := range tagFilters {
		tagSet[strings.ToLower(t)] = true
	}
	senderPrefix := strings.ToLower(senderFilter)

	var result []message.Message
	for _, m := range msgs {
		senderHex := fmt.Sprintf("%x", m.Sender)
		if !matchesSender(senderHex, senderPrefix) {
			continue
		}
		if len(tagSet) > 0 && !matchesTags(m.Tags, tagSet) {
			continue
		}
		result = append(result, m)
	}
	return result
}

// errNoReachablePeers is returned by runNATPoll when no non-empty peer endpoints exist.
var errNoReachablePeers = errors.New("no reachable peers to poll")

// runNATPoll is the NAT-mode poll loop. It polls the first reachable peer and
// prints received messages to w. When cfg.follow is false, it exits after the
// first successful response (even if empty). When cfg.follow is true, it loops
// until cfg.stopCh receives a signal.
func runNATPoll(cfg natPollConfig, w io.Writer) error {
	// Filter to peers with non-empty endpoints.
	var peers []store.PeerEndpoint
	for _, p := range cfg.peers {
		if p.Endpoint != "" {
			peers = append(peers, p)
		}
	}
	if len(peers) == 0 {
		return errNoReachablePeers
	}

	// Set up signal handling if no external stopCh was provided.
	stopCh := cfg.stopCh
	if stopCh == nil {
		stopCh = make(chan os.Signal, 1)
		signal.Notify(stopCh, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(stopCh)
	}

	cursor := cfg.cursor
	peerIdx := 0
	timeout := cfg.timeoutSecs
	if timeout <= 0 {
		timeout = 30
	}
	// firstErr records the first poll error in one-shot mode so it is reported
	// instead of the last error when all peers have been tried.
	var firstErr error
	// consecutiveErrors tracks how many peers have failed since the last success.
	// In one-shot mode, once this reaches len(peers) all peers have been tried
	// and the loop exits. This correctly handles both the single-peer case
	// (where peerIdx wraps to 0 immediately) and multi-peer cases with
	// alternating success/failure (where peerIdx never wraps back to 0).
	consecutiveErrors := 0

	for {
		// Check for stop signal (non-blocking).
		select {
		case <-stopCh:
			return nil
		default:
		}

		msgs, newCursor, err := cfhttp.Poll(peers[peerIdx].Endpoint, cfg.campfireID, cursor, timeout, cfg.id)
		if err != nil {
			// Rotate to next peer on error.
			peerIdx = (peerIdx + 1) % len(peers)
			consecutiveErrors++
			if !cfg.follow && firstErr == nil {
				firstErr = err
			}
			time.Sleep(1 * time.Second)
			// Re-check stop after sleep.
			select {
			case <-stopCh:
				return nil
			default:
			}
			if !cfg.follow {
				// In one-shot mode, do not retry indefinitely; return after
				// exhausting all peers once. Use consecutiveErrors rather than
				// peerIdx wrap-around: peerIdx wraps to 0 on the first error
				// when len(peers) == 1, so the index check would fire before
				// a second peer is tried. For multi-peer cases with alternating
				// success/failure, peerIdx may never wrap back to 0.
				if consecutiveErrors >= len(peers) {
					return fmt.Errorf("polling peers: %w", firstErr)
				}
			}
			continue
		}
		// Reset error tracking on success.
		consecutiveErrors = 0
		firstErr = nil

		if len(msgs) > 0 {
			cursor = newCursor
			filtered := filterNATMessages(msgs, cfg.tagFilters, cfg.senderFilter)
			if len(filtered) > 0 {
				printNATMessages(cfg.campfireID, filtered, w, cfg.st)
			}
		}

		if !cfg.follow {
			break
		}

		// Check stop signal before blocking again.
		select {
		case <-stopCh:
			return nil
		default:
		}
	}
	return nil
}
