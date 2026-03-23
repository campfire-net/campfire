package cards

// tagAccentColor returns the Adaptive Card color string for the first (primary) tag.
// This is used to set the overall card accent / background style.
func tagAccentColor(tags []string) string {
	if len(tags) == 0 {
		return "Default"
	}
	switch tags[0] {
	case "blocker":
		return "Attention" // red
	case "gate":
		return "Warning" // yellow
	case "status":
		return "Default" // gray
	case "schema-change":
		return "Accent" // blue
	case "finding":
		return "Warning" // orange — closest AC 1.4 color
	case "directive":
		return "Good" // green
	case "test-flaky":
		return "Attention" // red
	default:
		return "Default"
	}
}

// tagBadgeStyle returns the icon prefix and color for a tag badge in the header row.
func tagBadgeStyle(tag string) (icon string, color string) {
	switch tag {
	case "blocker":
		return "🔔 ", "Attention"
	case "gate":
		return "🔒 ", "Warning"
	case "status":
		return "", "Default"
	case "schema-change":
		return "⌨ ", "Accent"
	case "finding":
		return "🔍 ", "Warning"
	case "directive":
		return "★ ", "Good"
	case "test-flaky":
		return "[FLAKY] ", "Attention"
	default:
		return "", "Default"
	}
}

// ApplyTagStyling mutates a card map to apply tag-driven visual modifications.
// Called after BuildCard for cases where the caller wants to inspect or further adjust the card.
// BuildCard already incorporates tag styling inline; this function is an explicit hook for
// post-build adjustments (e.g., adding container styles based on first-tag accent).
func ApplyTagStyling(card map[string]any, tags []string) {
	if len(tags) == 0 {
		return
	}
	accent := tagAccentColor(tags)
	// Add a muted container style hint via the card's body background suggestion.
	// Adaptive Card 1.4 does not support card-level background color directly;
	// the accent is conveyed through text colors and icon badges already applied
	// inside BuildCard. This function is a no-op for now but is here as an extension point.
	_ = accent
}
