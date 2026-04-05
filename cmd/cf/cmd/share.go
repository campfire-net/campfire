package cmd

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/campfire-net/campfire/pkg/beacon"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/spf13/cobra"
)

var shareCmd = &cobra.Command{
	Use:   "share <campfire>",
	Short: "Output a portable beacon string for a campfire",
	Long: `Output a beacon:BASE64 string for the given campfire.
The string encodes the campfire's beacon and can be shared out-of-band.
To join using the string: cf join beacon:<string>`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runShare(args[0], BeaconDir())
	},
}

// runShare finds the beacon for campfirePrefix in beaconDir and prints
// the portable beacon:BASE64 string to stdout.
func runShare(campfirePrefix string, beaconDir string) error {
	b, err := findBeaconForCampfire(campfirePrefix, beaconDir)
	if err != nil {
		return err
	}

	data, err := cfencoding.Marshal(b)
	if err != nil {
		return fmt.Errorf("encoding beacon: %w", err)
	}

	fmt.Println("beacon:" + base64.StdEncoding.EncodeToString(data))
	return nil
}

// findBeaconForCampfire scans beaconDir for a beacon whose campfire ID starts
// with campfirePrefix. Returns an error if not found or ambiguous.
func findBeaconForCampfire(campfirePrefix string, beaconDir string) (*beacon.Beacon, error) {
	beacons, err := beacon.Scan(beaconDir)
	if err != nil {
		return nil, fmt.Errorf("scanning beacons: %w", err)
	}

	var matches []*beacon.Beacon
	for i := range beacons {
		if strings.HasPrefix(beacons[i].CampfireIDHex(), campfirePrefix) {
			b := beacons[i]
			matches = append(matches, &b)
		}
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no beacon found for campfire %s", campfirePrefix)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("ambiguous campfire prefix %s: %d matches", campfirePrefix, len(matches))
	}
}

// parseBeaconString decodes a beacon:BASE64 string and returns the Beacon.
// This is the inverse of the beacon:BASE64 format produced by cf share.
func parseBeaconString(s string) (*beacon.Beacon, error) {
	uri, err := naming.ParseURI(s)
	if err != nil {
		return nil, fmt.Errorf("parsing beacon string: %w", err)
	}
	if uri.Kind != naming.URIKindBeacon {
		return nil, fmt.Errorf("not a beacon string: must start with beacon:")
	}

	// Decode the base64 beacon data (try multiple encodings for interop).
	var raw []byte
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		raw, err = enc.DecodeString(uri.BeaconData)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("decoding beacon data: %w", err)
	}

	var b beacon.Beacon
	if err := cfencoding.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("unmarshaling beacon: %w", err)
	}
	return &b, nil
}

func init() {
	rootCmd.AddCommand(shareCmd)
}
