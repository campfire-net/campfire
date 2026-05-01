package cmd

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/pkg/beacon"
	cfidentity "github.com/campfire-net/campfire/cf-conventions/cf-convention-extensions/identity"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
	"github.com/spf13/cobra"
)

var homeCmd = &cobra.Command{
	Use:   "home",
	Short: "Manage identity home campfires",
	Long: `Manage identity home campfires.

  cf home link <campfire-id>   link two identity campfires via the echo ceremony

The home-link ceremony establishes a verified bidirectional link between this
campfire (your home) and another campfire, using the echo ceremony defined in
the Identity Convention v0.1.`,
}

// homeLinkRole is the role assigned to linked campfires in the declare-home operation.
const homeLinkRole = "secondary"

var homeLinkCmd = &cobra.Command{
	Use:   "link <campfire-id>",
	Short: "Link two identity campfires via the echo ceremony",
	Long: `Link this campfire (campfire A) to another campfire (campfire B) via the echo ceremony.

The ceremony posts declare-home on both campfires and publishes a cross-signed
echo message that allows third parties to verify the bidirectional link.

Steps:
  1. Post declare-home(B) on campfire A → message M_A
  2. Post declare-home(A) on campfire B → message M_B (references M_A)
  3. Post echo message on campfire A, signed by campfire B's private key
  4. Publish identity:v1 beacon on campfire A

Requires write access to BOTH campfires with threshold=1 (local filesystem
campfires where the agent holds the campfire private key).

Example:
  cf home link abc123...def456`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		// Resolve campfire B ID from argument.
		campfireBID, err := resolveCampfireID(args[0], s)
		if err != nil {
			return fmt.Errorf("resolving campfire B ID: %w", err)
		}

		// Resolve campfire A ID — the agent's home campfire (alias "home").
		campfireAID, err := resolveCampfireID("home", s)
		if err != nil {
			return fmt.Errorf("resolving home campfire A (alias 'home'): %w\n\nSet the 'home' alias with: cf alias set home <campfire-id>", err)
		}

		if campfireAID == campfireBID {
			return fmt.Errorf("campfire A and campfire B are the same campfire (%s)", campfireAID[:12])
		}

		// Verify the agent is a member of both campfires.
		mA, err := s.GetMembership(campfireAID)
		if err != nil || mA == nil {
			return fmt.Errorf("not a member of campfire A (%s): %w", campfireAID[:12], err)
		}
		mB, err := s.GetMembership(campfireBID)
		if err != nil || mB == nil {
			return fmt.Errorf("not a member of campfire B (%s): make sure you have joined it", campfireBID[:12])
		}

		// We need campfire B's private key to produce the cross-signed echo message.
		// Only filesystem-transport campfires (where the agent holds the campfire
		// private key) are supported for the echo ceremony.
		trB := fs.ForDir(mB.TransportDir)
		stateB, err := trB.ReadState(campfireBID)
		if err != nil {
			return fmt.Errorf("reading campfire B state: %w\n\nThe echo ceremony requires a filesystem-transport campfire where you hold the campfire private key", err)
		}
		if len(stateB.PrivateKey) == 0 {
			return fmt.Errorf("campfire B (%s) has no local private key — echo ceremony requires threshold=1 filesystem campfire", campfireBID[:12])
		}
		campfireBPrivKey := ed25519.PrivateKey(stateB.PrivateKey)
		campfireBPubKey := ed25519.PublicKey(stateB.PublicKey)

		// Also need campfire A's state for the beacon publication.
		trA := fs.ForDir(mA.TransportDir)
		stateA, err := trA.ReadState(campfireAID)
		if err != nil {
			return fmt.Errorf("reading campfire A state: %w", err)
		}
		if len(stateA.PrivateKey) == 0 {
			return fmt.Errorf("campfire A (%s) has no local private key — echo ceremony requires threshold=1 filesystem campfire", campfireAID[:12])
		}

		client := protocol.New(s, agentID)

		// Step 1: Post declare-home(B) on campfire A.
		declareHomePayload := map[string]string{
			"campfire_id": campfireBID,
			"role":        homeLinkRole,
		}
		declareHomeBPayloadBytes, err := json.Marshal(declareHomePayload)
		if err != nil {
			return fmt.Errorf("encoding declare-home payload: %w", err)
		}
		mA_msg, err := client.Send(protocol.SendRequest{
			CampfireID: campfireAID,
			Payload:    declareHomeBPayloadBytes,
			Tags:       []string{cfidentity.IdentityHomeDeclaredTag},
		})
		if err != nil {
			return fmt.Errorf("posting declare-home(B) on campfire A: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "step 1: posted declare-home(%s) on campfire A → M_A=%s\n", campfireBID[:12], mA_msg.ID[:8])

		// Step 2: Post declare-home(A) on campfire B, referencing M_A.
		declareHomeAPayload := map[string]string{
			"campfire_id":    campfireAID,
			"role":           homeLinkRole,
			"ref_message_id": mA_msg.ID,
		}
		declareHomeAPayloadBytes, err := json.Marshal(declareHomeAPayload)
		if err != nil {
			return fmt.Errorf("encoding declare-home(A) payload: %w", err)
		}
		mB_msg, err := client.Send(protocol.SendRequest{
			CampfireID: campfireBID,
			Payload:    declareHomeAPayloadBytes,
			Tags:       []string{cfidentity.IdentityHomeDeclaredTag},
		})
		if err != nil {
			return fmt.Errorf("posting declare-home(A) on campfire B: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "step 2: posted declare-home(%s) on campfire B → M_B=%s\n", campfireAID[:12], mB_msg.ID[:8])

		// Step 3: Post echo message on campfire A.
		// The echo is signed by campfire B's private key over M_B's ID bytes,
		// proving the operator of campfire B authorized the link.
		mBIDBytes := []byte(mB_msg.ID)
		signedByB := ed25519.Sign(campfireBPrivKey, mBIDBytes)
		echoPayload := map[string]string{
			"echo_of":     mB_msg.ID,
			"signed_by_b": hex.EncodeToString(signedByB),
			// Include campfire B's public key hex so verifiers don't need to
			// decode the campfire ID themselves.
			"campfire_b_pubkey": hex.EncodeToString(campfireBPubKey),
		}
		echoPayloadBytes, err := json.Marshal(echoPayload)
		if err != nil {
			return fmt.Errorf("encoding echo payload: %w", err)
		}
		echoMsg, err := client.Send(protocol.SendRequest{
			CampfireID: campfireAID,
			Payload:    echoPayloadBytes,
			Tags:       []string{cfidentity.IdentityHomeEchoTag},
			Antecedents: []string{mA_msg.ID},
		})
		if err != nil {
			return fmt.Errorf("posting echo message on campfire A: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "step 3: posted echo message on campfire A (signed by campfire B key) → echo=%s\n", echoMsg.ID[:8])

		// Step 4: Publish identity:v1 beacon on campfire A.
		b, err := beacon.New(
			ed25519.PublicKey(stateA.PublicKey),
			ed25519.PrivateKey(stateA.PrivateKey),
			stateA.JoinProtocol,
			stateA.ReceptionRequirements,
			beacon.TransportConfig{
				Protocol: "filesystem",
				Config:   map[string]string{"dir": trA.CampfireDir(campfireAID)},
			},
			cfidentity.IdentityBeaconTag,
		)
		if err != nil {
			return fmt.Errorf("creating identity:v1 beacon: %w", err)
		}
		beaconDir := BeaconDir()
		if err := beacon.Publish(beaconDir, b); err != nil {
			// Non-fatal: warn but don't fail the ceremony.
			fmt.Fprintf(os.Stderr, "warning: could not publish identity:v1 beacon: %v\n", err)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "step 4: published identity:v1 beacon for campfire A\n")
		}

		fmt.Fprintf(cmd.OutOrStdout(), "\nhome link complete:\n")
		fmt.Fprintf(cmd.OutOrStdout(), "  campfire A: %s\n", campfireAID[:12])
		fmt.Fprintf(cmd.OutOrStdout(), "  campfire B: %s\n", campfireBID[:12])
		fmt.Fprintf(cmd.OutOrStdout(), "  M_A: %s (declare-home(B) on A)\n", mA_msg.ID)
		fmt.Fprintf(cmd.OutOrStdout(), "  M_B: %s (declare-home(A) on B)\n", mB_msg.ID)
		fmt.Fprintf(cmd.OutOrStdout(), "  echo: %s (signed by campfire B key on A)\n", echoMsg.ID)
		fmt.Fprintf(cmd.OutOrStdout(), "\nthird-party verification:\n")
		fmt.Fprintf(cmd.OutOrStdout(), "  1. Read messages tagged %q on campfire A — see M_A\n", cfidentity.IdentityHomeDeclaredTag)
		fmt.Fprintf(cmd.OutOrStdout(), "  2. Read messages tagged %q on campfire B — see M_B\n", cfidentity.IdentityHomeDeclaredTag)
		fmt.Fprintf(cmd.OutOrStdout(), "  3. Read message tagged %q on campfire A — verify signed_by_b against campfire B pubkey\n", cfidentity.IdentityHomeEchoTag)

		if jsonOutput {
			out := map[string]interface{}{
				"campfire_a":         campfireAID,
				"campfire_b":         campfireBID,
				"declare_home_b_id":  mA_msg.ID,
				"declare_home_a_id":  mB_msg.ID,
				"echo_id":            echoMsg.ID,
				"campfire_b_pubkey":  hex.EncodeToString(campfireBPubKey),
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}
		return nil
	},
}

// homeRevokeCmd implements `cf home revoke <member-key>`.
// Evicts the key from the home campfire and posts a signed identity:revoked message.
var homeRevokeCmd = &cobra.Command{
	Use:   "revoke <member-key>",
	Short: "Revoke a linked identity (evict key + post identity:revoked)",
	Long: `Revoke a linked identity from this home campfire.

  cf home revoke <member-key>   evict key and post identity:revoked

The revoked key is removed from the home campfire's member list and a signed
identity:revoked message is posted so relying parties can learn about the
revocation (TTL-bounded, default 1h).

Note: machine-to-team links are NOT automatically revoked. Revoke separately
from any campfire where the key was admitted on your behalf.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		revokedKeyHex := args[0]

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		// Resolve home campfire.
		homeID, err := resolveCampfireID("home", s)
		if err != nil {
			return fmt.Errorf("resolving home campfire: %w\n\nSet the 'home' alias with: cf alias set home <campfire-id>", err)
		}

		// Check caller has admin or creator role in the home campfire.
		// Use the store membership record, which preserves the original role
		// assigned at join/create time (e.g. "creator").
		homeMembership, err := s.GetMembership(homeID)
		if err != nil || homeMembership == nil {
			return fmt.Errorf("checking home campfire membership: not a member of home campfire %s", homeID[:12])
		}
		if homeMembership.Role != "creator" && homeMembership.Role != "admin" {
			return fmt.Errorf("cf home revoke requires admin or creator role in the campfire (current role: %q)", homeMembership.Role)
		}

		client := protocol.New(s, agentID)

		// Evict the key from the home campfire.
		if _, err := client.Evict(protocol.EvictRequest{
			CampfireID:      homeID,
			MemberPubKeyHex: revokedKeyHex,
		}); err != nil {
			return fmt.Errorf("evicting key from home campfire: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Key %s evicted from home campfire %s\n", revokedKeyHex[:12], homeID[:12])

		// Post signed identity:revoked message.
		revokePayload := map[string]string{
			"revoked_key": revokedKeyHex,
			"home_id":     homeID,
		}
		revokePayloadBytes, err := json.Marshal(revokePayload)
		if err != nil {
			return fmt.Errorf("encoding revoke payload: %w", err)
		}
		revokeMsg, err := client.Send(protocol.SendRequest{
			CampfireID: homeID,
			Payload:    revokePayloadBytes,
			Tags:       []string{cfidentity.IdentityRevokedTag},
		})
		if err != nil {
			return fmt.Errorf("posting identity:revoked message: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Revocation posted → %s\n", revokeMsg.ID[:8])

		if jsonOutput {
			out := map[string]interface{}{
				"revoked_key": revokedKeyHex,
				"home_id":     homeID,
				"revoke_msg":  revokeMsg.ID,
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}
		return nil
	},
}

// homeDisplayCmd implements `cf home display`.
// Shows the current machine key.
var homeDisplayCmd = &cobra.Command{
	Use:   "display",
	Short: "Show current identity state",
	Long: `Show current identity state.

  cf home display   show machine key`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Load machine identity for pubkey display.
		agentID, err := loadIdentity()
		if err != nil {
			return fmt.Errorf("loading identity: %w", err)
		}

		machineKeyHex := hex.EncodeToString(agentID.PublicKey)

		fmt.Fprintf(cmd.OutOrStdout(), "Machine key:   %s...\n", machineKeyHex[:12])

		if jsonOutput {
			out := map[string]interface{}{
				"machine_key": machineKeyHex,
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}
		return nil
	},
}

// isInteractiveTTY returns true if stdin is an interactive terminal.
// CF_FORCE_INTERACTIVE=1 bypasses the TTY check (test use only).
func isInteractiveTTY() bool {
	if os.Getenv("CF_FORCE_INTERACTIVE") == "1" {
		return true
	}
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// checkLocalCampfireKey verifies that campfireID is locally initialized and
// its private key is present in the store. This proves the caller controls
// campfire B via local key access.
// Returns nil if the campfire has a local private key, or a descriptive error.
func checkLocalCampfireKey(s interface {
	GetMembership(string) (*store.Membership, error)
}, campfireID string) error {
	m, err := s.GetMembership(campfireID)
	if err != nil || m == nil {
		return fmt.Errorf("not a member of campfire %s", campfireID[:12])
	}
	tr := fs.ForDir(m.TransportDir)
	state, err := tr.ReadState(campfireID)
	if err != nil {
		return fmt.Errorf("reading campfire %s state: %w — requires a locally-initialized campfire where you hold the private key. Use 'cf home link' for remote campfires", campfireID[:12], err)
	}
	if len(state.PrivateKey) == 0 {
		return fmt.Errorf("campfire %s has no local private key — use 'cf home link' to perform the cross-campfire ceremony", campfireID[:12])
	}
	return nil
}

func init() {
	homeCmd.AddCommand(homeRevokeCmd)
	homeCmd.AddCommand(homeDisplayCmd)
	homeCmd.AddCommand(homeLinkCmd)
	rootCmd.AddCommand(homeCmd)
}
