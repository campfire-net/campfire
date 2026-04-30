package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

var sendCmd = &cobra.Command{
	Use:   "send [campfire-id] <message>",
	Short: "Send a message to a campfire",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		sendTags, _ := cmd.Flags().GetStringSlice("tag")
		sendAntecedents, _ := cmd.Flags().GetStringSlice("reply-to")
		sendFuture, _ := cmd.Flags().GetBool("future")
		sendFulfills, _ := cmd.Flags().GetString("fulfills")
		sendInstance, _ := cmd.Flags().GetString("instance")
		// Merge deprecated --antecedent alias into --reply-to.
		if legacyAnts, err := cmd.Flags().GetStringSlice("antecedent"); err == nil && len(legacyAnts) > 0 {
			sendAntecedents = append(sendAntecedents, legacyAnts...)
		}

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		var campfireID, payload string
		fromProjectRoot := false
		if len(args) == 2 {
			campfireID, err = resolveCampfireID(args[0], s)
			if err != nil {
				return err
			}
			payload = args[1]
		} else {
			// No campfire ID provided — try context, then project root.
			if ctxID, err := resolveImplicitCampfire(); err != nil {
				return err
			} else if ctxID != "" {
				campfireID = ctxID
			} else {
				id, _, ok := ProjectRoot()
				if !ok {
					return fmt.Errorf("campfire ID required: no context set and no .campfire/root found in this directory tree")
				}
				campfireID = id
				fromProjectRoot = true
			}
			payload = args[0]
		}

		m, err := s.GetMembership(campfireID)
		if err != nil {
			return fmt.Errorf("querying membership: %w", err)
		}
		if m == nil && fromProjectRoot {
			// Auto-join open-protocol root campfires.
			if err := autoJoinRootCampfire(campfireID, agentID, s); err != nil {
				return fmt.Errorf("auto-joining root campfire: %w", err)
			}
			m, err = s.GetMembership(campfireID)
			if err != nil {
				return fmt.Errorf("querying membership after auto-join: %w", err)
			}
		}
		if m == nil {
			return fmt.Errorf("not a member of campfire %s", campfireID[:12])
		}

		// Build tags
		tags := sendTags
		if sendFuture {
			tags = append(tags, "future")
		}
		if sendFulfills != "" {
			tags = append(tags, "fulfills")
		}

		// Build antecedents
		antecedents := sendAntecedents
		if sendFulfills != "" {
			antecedents = append(antecedents, sendFulfills)
		}

		// Validate convention:operation payloads before posting.
		for _, tag := range tags {
			if strings.EqualFold(tag, convention.ConventionOperationTag) {
				result := convention.Lint([]byte(payload))
				if len(result.Errors) > 0 {
					for _, f := range result.Errors {
						loc := ""
						if f.Field != "" {
							loc = " [" + f.Field + "]"
						}
						fmt.Fprintf(os.Stderr, "error%s: %s\n", loc, f.Message)
					}
					return fmt.Errorf("convention:operation payload is invalid (see errors above)")
				}
				for _, f := range result.Warnings {
					loc := ""
					if f.Field != "" {
						loc = " [" + f.Field + "]"
					}
					fmt.Fprintf(os.Stderr, "warning%s: %s\n", loc, f.Message)
				}
				break
			}
		}

		// Delegate to protocol.Client — handles transport dispatch, role enforcement,
		// message signing, and provenance hop.
		client := protocol.New(s, agentID)
		msg, err := client.Send(protocol.SendRequest{
			CampfireID:  campfireID,
			Payload:     []byte(payload),
			Tags:        tags,
			Antecedents: antecedents,
			Instance:    sendInstance,
		})
		if err != nil {
			return err
		}


		if jsonOutput {
			out := map[string]interface{}{
				"id":          msg.ID,
				"campfire_id": campfireID,
				"sender":      agentID.PublicKeyHex(),
				"payload":     payload,
				"tags":        msg.Tags,
				"antecedents": msg.Antecedents,
				"timestamp":   msg.Timestamp,
				"instance":    msg.Instance,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Println(msg.ID)
		return nil
	},
}

func init() {
	sendCmd.Flags().StringSlice("tag", nil, "message tags")
	sendCmd.Flags().StringSlice("reply-to", nil, "message IDs this message replies to (causal dependencies)")
	// --antecedent is a hidden backward-compatibility alias for --reply-to.
	sendCmd.Flags().StringSlice("antecedent", nil, "alias for --reply-to (deprecated)")
	sendCmd.Flags().MarkHidden("antecedent") //nolint:errcheck
	sendCmd.Flags().Bool("future", false, "tag this message as a future")
	sendCmd.Flags().String("fulfills", "", "message ID this fulfills (adds 'fulfills' tag + reply-to in one step)")
	sendCmd.Flags().String("instance", "", "sender instance/role name (tainted, not verified)")
	rootCmd.AddCommand(sendCmd)
}
