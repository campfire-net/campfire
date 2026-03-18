package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/spf13/cobra"
)

var inspectCmd = &cobra.Command{
	Use:   "inspect <message-id>",
	Short: "Show full provenance chain for a message",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		messageID := args[0]

		s, err := store.Open(store.StorePath(CFHome()))
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer s.Close()

		msg, err := s.GetMessage(messageID)
		if err != nil {
			return fmt.Errorf("querying message: %w", err)
		}
		if msg == nil {
			return fmt.Errorf("message %s not found (run 'cf read' first to sync)", messageID)
		}

		var provenance []message.ProvenanceHop
		if err := json.Unmarshal([]byte(msg.Provenance), &provenance); err != nil {
			return fmt.Errorf("parsing provenance: %w", err)
		}

		var antecedents []string
		json.Unmarshal([]byte(msg.Antecedents), &antecedents)
		if antecedents == nil {
			antecedents = []string{}
		}

		// Find messages that reference this one
		refs, _ := s.ListReferencingMessages(messageID)
		var referencedBy []string
		for _, ref := range refs {
			referencedBy = append(referencedBy, ref.ID)
		}
		if referencedBy == nil {
			referencedBy = []string{}
		}

		// Verify message signature
		msgSigValid := message.VerifyMessageSignature(messageID, msg.Payload, msg.Tags, msg.Antecedents, msg.Timestamp, msg.Sender, msg.Signature)

		if jsonOutput {
			type hopJSON struct {
				CampfireID            string   `json:"campfire_id"`
				MembershipHash        string   `json:"membership_hash"`
				MemberCount           int      `json:"member_count"`
				JoinProtocol          string   `json:"join_protocol"`
				ReceptionRequirements []string `json:"reception_requirements"`
				Timestamp             int64    `json:"timestamp"`
				SignatureValid        bool     `json:"signature_valid"`
			}
			type inspectJSON struct {
				ID             string    `json:"id"`
				CampfireID     string    `json:"campfire_id"`
				Sender         string    `json:"sender"`
				Instance       string    `json:"instance,omitempty"`
				Payload        string    `json:"payload"`
				Tags           []string  `json:"tags"`
				Antecedents    []string  `json:"antecedents"`
				ReferencedBy   []string  `json:"referenced_by"`
				Timestamp      int64     `json:"timestamp"`
				SignatureValid bool      `json:"signature_valid"`
				Provenance     []hopJSON `json:"provenance"`
			}
			var tags []string
			json.Unmarshal([]byte(msg.Tags), &tags)

			out := inspectJSON{
				ID:             msg.ID,
				CampfireID:     msg.CampfireID,
				Sender:         msg.Sender,
				Instance:       msg.Instance,
				Payload:        string(msg.Payload),
				Tags:           tags,
				Antecedents:    antecedents,
				ReferencedBy:   referencedBy,
				Timestamp:      msg.Timestamp,
				SignatureValid: msgSigValid,
			}
			for _, hop := range provenance {
				hopValid := message.VerifyHop(messageID, hop)
				out.Provenance = append(out.Provenance, hopJSON{
					CampfireID:            fmt.Sprintf("%x", hop.CampfireID),
					MembershipHash:        fmt.Sprintf("%x", hop.MembershipHash),
					MemberCount:           hop.MemberCount,
					JoinProtocol:          hop.JoinProtocol,
					ReceptionRequirements: hop.ReceptionRequirements,
					Timestamp:             hop.Timestamp,
					SignatureValid:        hopValid,
				})
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		// Human-readable output
		senderShort := msg.Sender
		if len(senderShort) > 12 {
			senderShort = senderShort[:12]
		}

		var tags []string
		json.Unmarshal([]byte(msg.Tags), &tags)

		sigStatus := "VALID"
		if !msgSigValid {
			sigStatus = "INVALID"
		}

		fmt.Printf("Message: %s\n", msg.ID)
		fmt.Printf("Campfire: %s\n", msg.CampfireID)
		fmt.Printf("Sender: %s\n", msg.Sender)
		if msg.Instance != "" {
			fmt.Printf("Instance: %s (tainted)\n", msg.Instance)
		}
		fmt.Printf("Signature: %s\n", sigStatus)
		fmt.Printf("Timestamp: %s\n", time.Unix(0, msg.Timestamp).Format("2006-01-02 15:04:05"))
		if len(tags) > 0 {
			fmt.Printf("Tags: %v\n", tags)
		}
		fmt.Printf("Payload: %s\n", string(msg.Payload))
		if len(antecedents) > 0 {
			fmt.Printf("Antecedents: %v\n", antecedents)
		}
		if len(referencedBy) > 0 {
			fmt.Printf("Referenced by: %v\n", referencedBy)
		}
		fmt.Println()

		if len(provenance) == 0 {
			fmt.Println("Provenance: (no hops)")
		} else {
			fmt.Printf("Provenance: %d hop(s)\n", len(provenance))
			for i, hop := range provenance {
				hopValid := message.VerifyHop(messageID, hop)
				hopStatus := "VALID"
				if !hopValid {
					hopStatus = "INVALID"
				}
				cfHex := fmt.Sprintf("%x", hop.CampfireID)
				cfShort := cfHex
				if len(cfShort) > 12 {
					cfShort = cfShort[:12]
				}
				hashHex := fmt.Sprintf("%x", hop.MembershipHash)
				hashShort := hashHex
				if len(hashShort) > 12 {
					hashShort = hashShort[:12]
				}
				fmt.Printf("  Hop %d:\n", i+1)
				fmt.Printf("    Campfire: %s\n", cfHex)
				fmt.Printf("    Signature: %s\n", hopStatus)
				fmt.Printf("    Members: %d (hash: %s...)\n", hop.MemberCount, hashShort)
				fmt.Printf("    Protocol: %s\n", hop.JoinProtocol)
				if len(hop.ReceptionRequirements) > 0 {
					fmt.Printf("    Required: %v\n", hop.ReceptionRequirements)
				}
				fmt.Printf("    Timestamp: %s\n", time.Unix(0, hop.Timestamp).Format("2006-01-02 15:04:05"))
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(inspectCmd)
}
