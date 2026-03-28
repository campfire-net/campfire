package durability

import (
	"testing"
	"time"
)

var referenceTime = time.Date(2026, 3, 28, 0, 0, 0, 0, time.UTC)

func strPtr(s string) *string             { return &s }
func ltPtr(l LifecycleType) *LifecycleType { return &l }

func TestVector_8_1_PersistentKeepForever(t *testing.T) {
	r := CheckDurabilityTags([]string{"category:infrastructure", "durability:max-ttl:0", "durability:lifecycle:persistent"}, referenceTime)
	assertValid(t, r); assertMaxTTL(t, r, "0"); assertLifecycle(t, r, LifecyclePersistent, nil); assertNoWarnings(t, r)
}

func TestVector_8_2_EphemeralSwarm(t *testing.T) {
	r := CheckDurabilityTags([]string{"category:infrastructure", "durability:max-ttl:4h", "durability:lifecycle:ephemeral:30m"}, referenceTime)
	assertValid(t, r); assertMaxTTL(t, r, "4h"); assertLifecycle(t, r, LifecycleEphemeral, strPtr("30m")); assertNoWarnings(t, r)
}

func TestVector_8_3_TimeBounded(t *testing.T) {
	r := CheckDurabilityTags([]string{"category:social", "durability:max-ttl:90d", "durability:lifecycle:bounded:2026-06-01T00:00:00Z"}, referenceTime)
	assertValid(t, r); assertMaxTTL(t, r, "90d"); assertLifecycle(t, r, LifecycleBounded, strPtr("2026-06-01T00:00:00Z")); assertNoWarnings(t, r)
}

func TestVector_8_4_NoDurabilityTags(t *testing.T) {
	r := CheckDurabilityTags([]string{"category:social", "member_count:5"}, referenceTime)
	assertValid(t, r)
	if r.MaxTTL != nil { t.Errorf("expected nil MaxTTL, got %q", *r.MaxTTL) }
	if r.LifecycleType != nil { t.Errorf("expected nil LifecycleType, got %q", *r.LifecycleType) }
	assertNoWarnings(t, r)
}

func TestVector_8_5_MaxTTLOnly(t *testing.T) {
	r := CheckDurabilityTags([]string{"category:social", "durability:max-ttl:30d"}, referenceTime)
	assertValid(t, r); assertMaxTTL(t, r, "30d")
	if r.LifecycleType != nil { t.Errorf("expected nil LifecycleType, got %q", *r.LifecycleType) }
	assertNoWarnings(t, r)
}

func TestVector_8_6_MultipleMaxTTL(t *testing.T) {
	r := CheckDurabilityTags([]string{"durability:max-ttl:30d", "durability:max-ttl:90d"}, referenceTime)
	assertInvalid(t, r, "multiple durability:max-ttl tags — at most one permitted")
}

func TestVector_8_7_UnknownUnit(t *testing.T) {
	r := CheckDurabilityTags([]string{"durability:max-ttl:30w"}, referenceTime)
	assertInvalid(t, r, "unknown unit 'w'")
}

func TestVector_8_8_ZeroWithUnit(t *testing.T) {
	r := CheckDurabilityTags([]string{"durability:max-ttl:0d"}, referenceTime)
	assertInvalid(t, r, "'0d' is invalid")
}

func TestVector_8_9_UnknownLifecycleType(t *testing.T) {
	r := CheckDurabilityTags([]string{"durability:lifecycle:temporary"}, referenceTime)
	assertInvalid(t, r, "unknown type 'temporary'")
}

func TestVector_8_10_MalformedBoundedDate(t *testing.T) {
	r := CheckDurabilityTags([]string{"durability:lifecycle:bounded:June-2026"}, referenceTime)
	assertInvalid(t, r, "bounded date \"June-2026\" is not valid ISO 8601 UTC")
}

func TestVector_8_11_PastBoundedDate(t *testing.T) {
	r := CheckDurabilityTags([]string{"durability:lifecycle:bounded:2025-01-01T00:00:00Z"}, referenceTime)
	assertValid(t, r); assertLifecycle(t, r, LifecycleBounded, strPtr("2025-01-01T00:00:00Z"))
	assertWarningContains(t, r, "bounded date is in the past")
}

func TestVector_8_12_Exceeds100Years(t *testing.T) {
	r := CheckDurabilityTags([]string{"durability:max-ttl:50000d"}, referenceTime)
	assertValid(t, r); assertMaxTTL(t, r, "50000d"); assertWarningContains(t, r, "exceeds 100 years")
}

func TestVector_8_13_EphemeralZero(t *testing.T) {
	r := CheckDurabilityTags([]string{"durability:lifecycle:ephemeral:0"}, referenceTime)
	assertInvalid(t, r, "ephemeral")
}

func TestVector_8_14_MultipleLifecycle(t *testing.T) {
	r := CheckDurabilityTags([]string{"durability:lifecycle:persistent", "durability:lifecycle:ephemeral:30m"}, referenceTime)
	assertInvalid(t, r, "multiple durability:lifecycle tags — at most one permitted")
}

func TestVector_8_15_NegativeDuration(t *testing.T) {
	r := CheckDurabilityTags([]string{"durability:max-ttl:-30d"}, referenceTime)
	assertInvalid(t, r, "duration must be non-negative")
}

func TestVector_8_16_LeadingZero(t *testing.T) {
	r := CheckDurabilityTags([]string{"durability:max-ttl:030d"}, referenceTime)
	assertInvalid(t, r, "leading zeros not permitted")
}

func TestVector_8_17_EphemeralZeroExplicit(t *testing.T) {
	r := CheckDurabilityTags([]string{"durability:lifecycle:ephemeral:0"}, referenceTime)
	assertInvalid(t, r, "ephemeral:0 is invalid")
}

func TestVector_8_18_UnknownNamespaceTag(t *testing.T) {
	r := CheckDurabilityTags([]string{"durability:backup:s3"}, referenceTime)
	assertValid(t, r); assertWarningContains(t, r, "unknown durability namespace tag")
}

func TestVector_8_19_ExceedsMaxDigits(t *testing.T) {
	r := CheckDurabilityTags([]string{"durability:max-ttl:1000000d"}, referenceTime)
	assertInvalid(t, r, "N exceeds 6 digits")
}

// --- URICacheTTL tests ---

func TestURICacheTTL_KeepForever(t *testing.T) {
	if got := URICacheTTL("0", time.Hour); got != time.Hour { t.Errorf("expected 1h, got %v", got) }
}
func TestURICacheTTL_ShorterThanDefault(t *testing.T) {
	if got := URICacheTTL("5m", time.Hour); got != 5*time.Minute { t.Errorf("expected 5m, got %v", got) }
}
func TestURICacheTTL_FloorEnforced(t *testing.T) {
	if got := URICacheTTL("10s", time.Hour); got != 60*time.Second { t.Errorf("expected 60s, got %v", got) }
}
func TestURICacheTTL_NoMetadata(t *testing.T) {
	if got := URICacheTTL("", time.Hour); got != time.Hour { t.Errorf("expected 1h, got %v", got) }
}
func TestURICacheTTL_DefaultBelowFloor(t *testing.T) {
	if got := URICacheTTL("", 30*time.Second); got != 60*time.Second { t.Errorf("expected 60s, got %v", got) }
}
func TestURICacheTTL_LongerThanDefault(t *testing.T) {
	if got := URICacheTTL("2h", time.Hour); got != time.Hour { t.Errorf("expected 1h, got %v", got) }
}

// --- helpers ---

func assertValid(t *testing.T, r DurabilityResult) { t.Helper(); if !r.Valid { t.Errorf("expected valid, got invalid: %s", r.Reason) } }
func assertInvalid(t *testing.T, r DurabilityResult, substr string) {
	t.Helper()
	if r.Valid { t.Fatalf("expected invalid containing %q, got valid", substr) }
	if !contains(r.Reason, substr) { t.Errorf("expected reason containing %q, got %q", substr, r.Reason) }
}
func assertMaxTTL(t *testing.T, r DurabilityResult, want string) {
	t.Helper()
	if r.MaxTTL == nil { t.Fatalf("expected MaxTTL %q, got nil", want) }
	if *r.MaxTTL != want { t.Errorf("expected MaxTTL %q, got %q", want, *r.MaxTTL) }
}
func assertLifecycle(t *testing.T, r DurabilityResult, wantType LifecycleType, wantValue *string) {
	t.Helper()
	if r.LifecycleType == nil { t.Fatalf("expected LifecycleType %q, got nil", wantType) }
	if *r.LifecycleType != wantType { t.Errorf("expected LifecycleType %q, got %q", wantType, *r.LifecycleType) }
	if wantValue == nil && r.LifecycleValue != nil { t.Errorf("expected nil LifecycleValue, got %q", *r.LifecycleValue) }
	if wantValue != nil {
		if r.LifecycleValue == nil { t.Fatalf("expected LifecycleValue %q, got nil", *wantValue) }
		if *r.LifecycleValue != *wantValue { t.Errorf("expected LifecycleValue %q, got %q", *wantValue, *r.LifecycleValue) }
	}
}
func assertNoWarnings(t *testing.T, r DurabilityResult) { t.Helper(); if len(r.Warnings) > 0 { t.Errorf("expected no warnings, got %v", r.Warnings) } }
func assertWarningContains(t *testing.T, r DurabilityResult, substr string) {
	t.Helper()
	for _, w := range r.Warnings { if contains(w, substr) { return } }
	t.Errorf("expected warning containing %q, got %v", substr, r.Warnings)
}
func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ { if s[i:i+len(sub)] == sub { return true } }
	return false
}
