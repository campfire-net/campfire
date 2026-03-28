package beacon

import (
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/durability"
)

var testNow = time.Date(2026, 3, 28, 0, 0, 0, 0, time.UTC)

func TestCheckBeaconDurability_ValidPersistent(t *testing.T) {
	d, err := CheckBeaconDurability([]string{"routing:beacon", "durability:max-ttl:0", "durability:lifecycle:persistent"}, testNow)
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if d == nil { t.Fatal("expected non-nil durability") }
	if d.MaxTTL == nil || *d.MaxTTL != "0" { t.Errorf("expected MaxTTL=0, got %v", d.MaxTTL) }
	if d.LifecycleType == nil || *d.LifecycleType != durability.LifecyclePersistent { t.Errorf("expected persistent lifecycle") }
}

func TestCheckBeaconDurability_NoDurabilityTags(t *testing.T) {
	d, err := CheckBeaconDurability([]string{"routing:beacon"}, testNow)
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if d != nil { t.Errorf("expected nil durability, got %+v", d) }
}

func TestCheckBeaconDurability_MalformedRejectsBeacon(t *testing.T) {
	_, err := CheckBeaconDurability([]string{"routing:beacon", "durability:max-ttl:30d", "durability:max-ttl:90d"}, testNow)
	if err == nil { t.Fatal("expected error for duplicate max-ttl tags") }
	de, ok := err.(*DurabilityError)
	if !ok { t.Fatalf("expected *DurabilityError, got %T", err) }
	if de.Reason == "" { t.Error("expected non-empty reason") }
}

func TestCheckBeaconDurability_WarningsPassThrough(t *testing.T) {
	d, err := CheckBeaconDurability([]string{"routing:beacon", "durability:lifecycle:bounded:2025-01-01T00:00:00Z"}, testNow)
	if err != nil { t.Fatalf("unexpected error: %v", err) }
	if d == nil { t.Fatal("expected non-nil durability") }
	if len(d.Warnings) == 0 { t.Error("expected warnings for past bounded date") }
}
