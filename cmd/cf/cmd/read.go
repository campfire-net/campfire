package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/3dl-dev/campfire/pkg/identity"
	"github.com/3dl-dev/campfire/pkg/store"
	cfhttp "github.com/3dl-dev/campfire/pkg/transport/http"
	"github.com/3dl-dev/campfire/pkg/transport/fs"
	"github.com/spf13/cobra"
)

var (
	readAll  bool
	readPeek bool
)

var readCmd = &cobra.Command{
	Use:   "read [campfire-id]",
	Short: "Read messages",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		agentID, err := identity.Load(IdentityPath())
		if err != nil {
			return fmt.Errorf("loading identity: %w", err)
		}

		s, err := store.Open(store.StorePath(CFHome()))
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer s.Close()

		// Determine which campfires to read from.
		var campfireIDs []string
		if len(args) > 0 {
			campfireIDs = []string{args[0]}
		} else {
			memberships, err := s.ListMemberships()
			if err != nil {
				return fmt.Errorf("listing memberships: %w", err)
			}
			for _, m := range memberships {
				campfireIDs = append(campfireIDs, m.CampfireID)
			}
		}

		// Sync messages for each campfire.
		for _, cfID := range campfireIDs {
			m, err := s.GetMembership(cfID)
			if err != nil || m == nil {
				continue
			}

			if isPeerHTTPCampfire(m.TransportDir, cfID) {
				// P2P HTTP transport: sync from all known peers.
				syncFromHTTPPeers(cfID, agentID, s)
			} else {
				// Filesystem transport: read from filesystem transport directory.
				syncFromFilesystem(cfID, s)
			}
		}

		// Query messages.
		var allMessages []store.MessageRecord
		for _, cfID := range campfireIDs {
			var afterTS int64
			if !readAll {
				afterTS, _ = s.GetReadCursor(cfID)
			}
			msgs, err := s.ListMessages(cfID, afterTS)
			if err != nil {
				return fmt.Errorf("listing messages: %w", err)
			}
			allMessages = append(allMessages, msgs...)
		}

		if jsonOutput {
			type jsonMsg struct {
				ID          string          `json:"id"`
				CampfireID  string          `json:"campfire_id"`
				Sender      string          `json:"sender"`
				Payload     string          `json:"payload"`
				Tags        []string        `json:"tags"`
				Antecedents []string        `json:"antecedents"`
				Timestamp   int64           `json:"timestamp"`
				Provenance  json.RawMessage `json:"provenance"`
			}
			var out []jsonMsg
			for _, m := range allMessages {
				var tags []string
				json.Unmarshal([]byte(m.Tags), &tags)
				var antecedents []string
				json.Unmarshal([]byte(m.Antecedents), &antecedents)
				if antecedents == nil {
					antecedents = []string{}
				}
				out = append(out, jsonMsg{
					ID:          m.ID,
					CampfireID:  m.CampfireID,
					Sender:      m.Sender,
					Payload:     string(m.Payload),
					Tags:        tags,
					Antecedents: antecedents,
					Timestamp:   m.Timestamp,
					Provenance:  json.RawMessage(m.Provenance),
				})
			}
			if out == nil {
				out = []jsonMsg{}
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(out); err != nil {
				return err
			}
		} else {
			if len(allMessages) == 0 {
				fmt.Println("No new messages.")
			}
			for _, m := range allMessages {
				var tags []string
				json.Unmarshal([]byte(m.Tags), &tags)
				var antecedents []string
				json.Unmarshal([]byte(m.Antecedents), &antecedents)

				cfShort := m.CampfireID
				if len(cfShort) > 6 {
					cfShort = cfShort[:6]
				}
				senderShort := m.Sender
				if len(senderShort) > 6 {
					senderShort = senderShort[:6]
				}
				ts := time.Unix(0, m.Timestamp).Format("2006-01-02 15:04:05")

				// Status markers
				var markers []string
				for _, t := range tags {
					if t == "future" {
						// Check if fulfilled
						refs, _ := s.ListReferencingMessages(m.ID)
						fulfilled := false
						for _, ref := range refs {
							var refTags []string
							json.Unmarshal([]byte(ref.Tags), &refTags)
							for _, rt := range refTags {
								if rt == "fulfills" {
									fulfilled = true
								}
							}
						}
						if fulfilled {
							markers = append(markers, "fulfilled")
						} else {
							markers = append(markers, "future")
						}
					}
				}

				statusStr := ""
				if len(markers) > 0 {
					statusStr = " [" + strings.Join(markers, ", ") + "]"
				}

				fmt.Printf("[campfire:%s] %s agent:%s%s\n", cfShort, ts, senderShort, statusStr)
				if len(tags) > 0 {
					fmt.Printf("  tags: %s\n", strings.Join(tags, ", "))
				}
				if len(antecedents) > 0 {
					shortAnts := make([]string, len(antecedents))
					for i, a := range antecedents {
						if len(a) > 8 {
							shortAnts[i] = a[:8]
						} else {
							shortAnts[i] = a
						}
					}
					fmt.Printf("  antecedents: %s\n", strings.Join(shortAnts, ", "))
				}
				fmt.Printf("  %s\n\n", string(m.Payload))
			}
		}

		// Update read cursors (unless --all or --peek).
		if !readAll && !readPeek && len(allMessages) > 0 {
			cursors := map[string]int64{}
			for _, m := range allMessages {
				if m.Timestamp > cursors[m.CampfireID] {
					cursors[m.CampfireID] = m.Timestamp
				}
			}
			for cfID, ts := range cursors {
				s.SetReadCursor(cfID, ts)
			}
		}

		return nil
	},
}

// syncFromFilesystem reads messages from the filesystem transport into the local store.
func syncFromFilesystem(cfID string, s *store.Store) {
	transport := fs.New(fs.DefaultBaseDir())
	fsMessages, err := transport.ListMessages(cfID)
	if err != nil {
		return
	}
	for _, fsMsg := range fsMessages {
		provJSON, err := json.Marshal(fsMsg.Provenance)
		if err != nil {
			continue
		}
		tagsJSON, err := json.Marshal(fsMsg.Tags)
		if err != nil {
			continue
		}
		antJSON, err := json.Marshal(fsMsg.Antecedents)
		if err != nil {
			continue
		}
		s.AddMessage(store.MessageRecord{ //nolint:errcheck
			ID:          fsMsg.ID,
			CampfireID:  cfID,
			Sender:      fmt.Sprintf("%x", fsMsg.Sender),
			Payload:     fsMsg.Payload,
			Tags:        string(tagsJSON),
			Antecedents: string(antJSON),
			Timestamp:   fsMsg.Timestamp,
			Signature:   fsMsg.Signature,
			Provenance:  string(provJSON),
			ReceivedAt:  store.NowNano(),
		})
	}
}

// syncFromHTTPPeers pulls messages from all known peer endpoints for a p2p-http campfire.
func syncFromHTTPPeers(cfID string, agentID *identity.Identity, s *store.Store) {
	peers, err := s.ListPeerEndpoints(cfID)
	if err != nil {
		return
	}

	// Get the sync cursor for this campfire.
	since, _ := s.GetReadCursor(cfID)

	for _, peer := range peers {
		if peer.MemberPubkey == agentID.PublicKeyHex() || peer.Endpoint == "" {
			continue
		}
		msgs, err := cfhttp.Sync(peer.Endpoint, cfID, since, agentID)
		if err != nil {
			// Non-fatal: peer may be offline.
			continue
		}
		for _, msg := range msgs {
			tagsJSON, _ := json.Marshal(msg.Tags)
			anteJSON, _ := json.Marshal(msg.Antecedents)
			provJSON, _ := json.Marshal(msg.Provenance)
			s.AddMessage(store.MessageRecord{ //nolint:errcheck
				ID:          msg.ID,
				CampfireID:  cfID,
				Sender:      fmt.Sprintf("%x", msg.Sender),
				Payload:     msg.Payload,
				Tags:        string(tagsJSON),
				Antecedents: string(anteJSON),
				Timestamp:   msg.Timestamp,
				Signature:   msg.Signature,
				Provenance:  string(provJSON),
				ReceivedAt:  store.NowNano(),
			})
		}
	}
}

func init() {
	readCmd.Flags().BoolVar(&readAll, "all", false, "show all messages (not just unread)")
	readCmd.Flags().BoolVar(&readPeek, "peek", false, "show unread messages without updating cursor")
	rootCmd.AddCommand(readCmd)
}
