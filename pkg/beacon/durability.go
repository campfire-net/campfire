package beacon

import (
	"time"

	"github.com/campfire-net/campfire/pkg/durability"
)

// BeaconDurability holds durability metadata extracted from beacon message tags.
type BeaconDurability struct {
	MaxTTL         *string
	LifecycleType  *durability.LifecycleType
	LifecycleValue *string
	Warnings       []string
}

// CheckBeaconDurability validates durability tags on a beacon message.
// It returns the parsed durability metadata if valid, or an error if
// malformed durability tags are present (e.g., multiple max-ttl tags).
// A nil result with no error means no durability tags were present.
func CheckBeaconDurability(msgTags []string, now time.Time) (*BeaconDurability, error) {
	result := durability.CheckDurabilityTags(msgTags, now)
	if !result.Valid {
		return nil, &DurabilityError{Reason: result.Reason}
	}
	if result.MaxTTL == nil && result.LifecycleType == nil && len(result.Warnings) == 0 {
		return nil, nil
	}
	return &BeaconDurability{
		MaxTTL:         result.MaxTTL,
		LifecycleType:  result.LifecycleType,
		LifecycleValue: result.LifecycleValue,
		Warnings:       result.Warnings,
	}, nil
}

// DurabilityError is returned when beacon durability tags are malformed.
type DurabilityError struct {
	Reason string
}

func (e *DurabilityError) Error() string {
	return "beacon durability: " + e.Reason
}
