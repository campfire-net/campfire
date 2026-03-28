// Package durability implements the Campfire Durability Convention v0.1.
//
// It provides parsing and conformance checking for durability tags
// (durability:max-ttl:* and durability:lifecycle:*) on beacon registrations.
package durability

import (
	"fmt"
	"strings"
	"time"
)

// LifecycleType is the campfire's continuity intention.
type LifecycleType string

const (
	LifecyclePersistent LifecycleType = "persistent"
	LifecycleEphemeral  LifecycleType = "ephemeral"
	LifecycleBounded    LifecycleType = "bounded"
)

const (
	maxTTLPrefix       = "durability:max-ttl:"
	lifecyclePrefix    = "durability:lifecycle:"
	durabilityPrefix   = "durability:"
	maxDigits          = 6
	maxDurationDays    = 36500 // 100 years
	minCacheTTLSeconds = 60
)

// DurabilityResult is the output of the conformance checker.
type DurabilityResult struct {
	Valid          bool
	Reason         string
	MaxTTL         *string        // normalized duration string or nil
	LifecycleType  *LifecycleType // persistent, ephemeral, bounded, or nil
	LifecycleValue *string        // timeout or date for ephemeral/bounded, nil otherwise
	Warnings       []string
}

// CheckDurabilityTags validates durability tags in a beacon's tag set.
// It runs as a post-pass after existing beacon-registration conformance checks.
func CheckDurabilityTags(tags []string, now time.Time) DurabilityResult {
	result := DurabilityResult{Valid: true}

	var maxTTLValues []string
	var lifecycleValues []string

	for _, tag := range tags {
		if strings.HasPrefix(tag, maxTTLPrefix) {
			maxTTLValues = append(maxTTLValues, strings.TrimPrefix(tag, maxTTLPrefix))
		} else if strings.HasPrefix(tag, lifecyclePrefix) {
			lifecycleValues = append(lifecycleValues, strings.TrimPrefix(tag, lifecyclePrefix))
		} else if strings.HasPrefix(tag, durabilityPrefix) {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("unknown durability namespace tag %q — reserved prefix reserved by convention", tag))
		}
	}

	// Check 1: max-ttl cardinality.
	if len(maxTTLValues) > 1 {
		return DurabilityResult{
			Valid:  false,
			Reason: "multiple durability:max-ttl tags — at most one permitted",
		}
	}

	// Check 2: max-ttl format.
	if len(maxTTLValues) == 1 {
		v := maxTTLValues[0]
		dur, err := ParseMaxTTL(v)
		if err != nil {
			return DurabilityResult{
				Valid:  false,
				Reason: fmt.Sprintf("durability:max-ttl: %s", err.Error()),
			}
		}
		result.MaxTTL = &v
		if dur > time.Duration(maxDurationDays)*24*time.Hour && v != "0" {
			result.Warnings = append(result.Warnings,
				"durability:max-ttl: duration exceeds 100 years — treating as keep-forever")
		}
	}

	// Check 3: lifecycle cardinality.
	if len(lifecycleValues) > 1 {
		return DurabilityResult{
			Valid:  false,
			Reason: "multiple durability:lifecycle tags — at most one permitted",
		}
	}

	// Check 4: lifecycle type.
	if len(lifecycleValues) == 1 {
		lt, lv, err := ParseLifecycle(lifecycleValues[0])
		if err != nil {
			return DurabilityResult{
				Valid:  false,
				Reason: fmt.Sprintf("durability:lifecycle: %s", err.Error()),
			}
		}
		result.LifecycleType = &lt
		if lv != "" {
			result.LifecycleValue = &lv
		}
		if lt == LifecycleBounded {
			t, err := time.Parse(time.RFC3339, lv)
			if err == nil && t.Before(now) {
				result.Warnings = append(result.Warnings,
					"durability:lifecycle:bounded date is in the past — campfire lifecycle has elapsed")
			}
		}
	}

	return result
}

// ParseMaxTTL parses a max-ttl duration value.
// "0" means keep forever. "<N><unit>" where unit is s/m/h/d.
func ParseMaxTTL(s string) (time.Duration, error) {
	if s == "0" {
		return 0, nil
	}
	if len(s) == 0 {
		return 0, fmt.Errorf("empty duration")
	}
	if s[0] == '-' {
		return 0, fmt.Errorf("duration must be non-negative")
	}

	unit := s[len(s)-1]
	nStr := s[:len(s)-1]

	if len(nStr) == 0 {
		return 0, fmt.Errorf("missing N in duration %q", s)
	}

	var multiplier time.Duration
	switch unit {
	case 's':
		multiplier = time.Second
	case 'm':
		multiplier = time.Minute
	case 'h':
		multiplier = time.Hour
	case 'd':
		multiplier = 24 * time.Hour
	default:
		return 0, fmt.Errorf("unknown unit '%s' — must be s, m, h, or d", string(unit))
	}

	if err := validateN(nStr); err != nil {
		return 0, err
	}

	n := parseDigits(nStr)
	if n == 0 {
		return 0, fmt.Errorf("'%s' is invalid — use '0' for keep-forever, or a positive integer with unit", s)
	}

	return time.Duration(n) * multiplier, nil
}

// ParseLifecycle parses a lifecycle value.
// Returns the type, the value (timeout or date), and any error.
func ParseLifecycle(s string) (LifecycleType, string, error) {
	switch {
	case s == "persistent":
		return LifecyclePersistent, "", nil

	case strings.HasPrefix(s, "ephemeral:"):
		timeout := strings.TrimPrefix(s, "ephemeral:")
		if timeout == "0" {
			return "", "", fmt.Errorf("ephemeral:0 is invalid — ephemeral requires a positive timeout")
		}
		_, err := ParseMaxTTL(timeout)
		if err != nil {
			if timeout == "" {
				return "", "", fmt.Errorf("ephemeral requires a positive timeout — use 'persistent' for no timeout")
			}
			return "", "", fmt.Errorf("ephemeral timeout: %w", err)
		}
		return LifecycleEphemeral, timeout, nil

	case strings.HasPrefix(s, "bounded:"):
		dateStr := strings.TrimPrefix(s, "bounded:")
		_, err := time.Parse(time.RFC3339, dateStr)
		if err != nil {
			return "", "", fmt.Errorf("bounded date %q is not valid ISO 8601 UTC", dateStr)
		}
		return LifecycleBounded, dateStr, nil

	default:
		return "", "", fmt.Errorf("unknown type '%s' — must be persistent, ephemeral:<duration>, or bounded:<iso8601>", s)
	}
}

// URICacheTTL computes the cache TTL for a resolved campfire with durability metadata.
// Returns max(60s, min(maxTTL, defaultTTL)). The 60s floor prevents cache TTL downgrade attacks.
func URICacheTTL(maxTTL string, defaultTTL time.Duration) time.Duration {
	floor := time.Duration(minCacheTTLSeconds) * time.Second

	if maxTTL == "" {
		if defaultTTL < floor {
			return floor
		}
		return defaultTTL
	}

	dur, err := ParseMaxTTL(maxTTL)
	if err != nil {
		if defaultTTL < floor {
			return floor
		}
		return defaultTTL
	}

	if dur == 0 {
		if defaultTTL < floor {
			return floor
		}
		return defaultTTL
	}

	result := defaultTTL
	if dur < result {
		result = dur
	}

	if result < floor {
		return floor
	}
	return result
}

func validateN(s string) error {
	if len(s) == 0 {
		return fmt.Errorf("missing N in duration")
	}
	if len(s) > maxDigits {
		return fmt.Errorf("N exceeds %d digits (prevents overflow)", maxDigits)
	}
	if len(s) > 1 && s[0] == '0' {
		return fmt.Errorf("leading zeros not permitted in N")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return fmt.Errorf("N must be a positive integer")
		}
	}
	return nil
}

func parseDigits(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
