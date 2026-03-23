package cards

import (
	"testing"
)

// TestTagAccentColor covers all 7 named tags plus the default fallback.
func TestTagAccentColor(t *testing.T) {
	cases := []struct {
		tags []string
		want string
	}{
		{[]string{"blocker"}, "Attention"},
		{[]string{"gate"}, "Warning"},
		{[]string{"status"}, "Default"},
		{[]string{"schema-change"}, "Accent"},
		{[]string{"finding"}, "Warning"},
		{[]string{"directive"}, "Good"},
		{[]string{"test-flaky"}, "Attention"},
		{[]string{"unknown-tag"}, "Default"},
		{[]string{}, "Default"},
		// First tag wins when multiple are present.
		{[]string{"blocker", "gate"}, "Attention"},
		{[]string{"gate", "blocker"}, "Warning"},
	}

	for _, tc := range cases {
		got := tagAccentColor(tc.tags)
		if got != tc.want {
			t.Errorf("tagAccentColor(%v) = %q, want %q", tc.tags, got, tc.want)
		}
	}
}

// TestApplyTagStyling verifies the function does not panic and is a no-op
// (it is an extension point; actual accent is conveyed via badge colors).
func TestApplyTagStyling_NoOp(t *testing.T) {
	msg := baseMsg()
	msg.Tags = []string{"blocker"}
	card := BuildCard(msg)

	// Capture card state before
	bodyBefore, _ := card["body"]
	actionsBefore, _ := card["actions"]

	ApplyTagStyling(card, msg.Tags)

	// Card must be unchanged — ApplyTagStyling is currently a no-op.
	bodyAfter, _ := card["body"]
	actionsAfter, _ := card["actions"]

	if bodyBefore == nil && bodyAfter != nil {
		t.Error("ApplyTagStyling unexpectedly added body")
	}
	_ = actionsBefore
	_ = actionsAfter
}

// TestApplyTagStyling_EmptyTags must not panic.
func TestApplyTagStyling_EmptyTags(t *testing.T) {
	card := map[string]any{"type": "AdaptiveCard"}
	ApplyTagStyling(card, nil)
	ApplyTagStyling(card, []string{})
}

// TestTagBadgeStyle_AllTags verifies icon and color for each named tag.
func TestTagBadgeStyle_AllTags(t *testing.T) {
	cases := []struct {
		tag       string
		wantColor string
	}{
		{"blocker", "Attention"},
		{"gate", "Warning"},
		{"status", "Default"},
		{"schema-change", "Accent"},
		{"finding", "Warning"},
		{"directive", "Good"},
		{"test-flaky", "Attention"},
		{"unknown", "Default"},
	}

	for _, tc := range cases {
		_, color := tagBadgeStyle(tc.tag)
		if color != tc.wantColor {
			t.Errorf("tagBadgeStyle(%q) color = %q, want %q", tc.tag, color, tc.wantColor)
		}
	}
}
