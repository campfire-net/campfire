package cards

import (
	"encoding/json"
	"testing"
	"time"
)

// baseMsg returns a minimal valid EnrichedMessage for tests.
func baseMsg() EnrichedMessage {
	return EnrichedMessage{
		SenderName:      "Atlas",
		SenderRole:      "ceo",
		SenderColor:     "Warning",
		Instance:        "ceo",
		CampfireShortID: "c1a628",
		Tags:            nil,
		Payload:         "Hello campfire.",
		Timestamp:       time.Now().Add(-5 * time.Minute),
		MessageID:       "deadbeefcafe1234",
		CampfireID:      "c1a62854df1bc8ee0550555205a9e9b479e3ae34ffb7929d2f42cb3f9793d310",
	}
}

// marshalRoundtrip serializes the card to JSON and back to verify it is valid JSON.
func marshalRoundtrip(t *testing.T, card map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("card failed JSON marshal: %v", err)
	}
	// verify it can unmarshal back
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("card failed JSON unmarshal: %v", err)
	}
	return b
}

// requireField asserts that a nested field exists with the expected value.
func requireField(t *testing.T, m map[string]any, key string, want any) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Errorf("missing field %q", key)
		return
	}
	if v != want {
		t.Errorf("field %q = %v, want %v", key, v, want)
	}
}

// requireFieldExists asserts that a key is present (value not checked).
func requireFieldExists(t *testing.T, m map[string]any, key string) {
	t.Helper()
	if _, ok := m[key]; !ok {
		t.Errorf("missing field %q", key)
	}
}

// TestBaseCardStructure verifies required top-level fields.
func TestBaseCardStructure(t *testing.T) {
	msg := baseMsg()
	card := BuildCard(msg)
	b := marshalRoundtrip(t, card)
	_ = b

	requireField(t, card, "$schema", "http://adaptivecards.io/schemas/adaptive-card.json")
	requireField(t, card, "type", "AdaptiveCard")
	requireField(t, card, "version", "1.4")
	requireFieldExists(t, card, "body")

	body, ok := card["body"].([]any)
	if !ok {
		t.Fatal("body is not []any")
	}
	if len(body) < 2 {
		t.Errorf("body has %d elements, want at least 2 (header + payload)", len(body))
	}

	// First body element must be a ColumnSet (header)
	header, ok := body[0].(map[string]any)
	if !ok {
		t.Fatal("body[0] is not map[string]any")
	}
	requireField(t, header, "type", "ColumnSet")

	columns, ok := header["columns"].([]any)
	if !ok {
		t.Fatal("header columns is not []any")
	}
	if len(columns) != 3 {
		t.Errorf("header has %d columns, want 3", len(columns))
	}
}

// TestBlockerCard checks red accent badge and no gate actions.
func TestBlockerCard(t *testing.T) {
	msg := baseMsg()
	msg.Tags = []string{"blocker"}
	card := BuildCard(msg)
	marshalRoundtrip(t, card)

	body := card["body"].([]any)

	// Second element should be tag badges ColumnSet
	badgesRow, ok := body[1].(map[string]any)
	if !ok {
		t.Fatal("body[1] (tag badges) is not map[string]any")
	}
	requireField(t, badgesRow, "type", "ColumnSet")

	badgeCols := badgesRow["columns"].([]any)
	if len(badgeCols) != 1 {
		t.Errorf("got %d badge columns, want 1", len(badgeCols))
	}

	// Verify badge color is Attention (red)
	col := badgeCols[0].(map[string]any)
	items := col["items"].([]any)
	tb := items[0].(map[string]any)
	requireField(t, tb, "color", "Attention")

	// Payload block should have color Attention
	payloadEl := body[2].(map[string]any)
	requireField(t, payloadEl, "color", "Attention")

	// No gate actions
	if _, ok := card["actions"]; ok {
		t.Error("blocker card should not have top-level actions")
	}
}

// TestGateCard verifies Approve/Reject Action.Submit buttons with correct data.
func TestGateCard(t *testing.T) {
	msg := baseMsg()
	msg.Tags = []string{"gate"}
	card := BuildCard(msg)
	marshalRoundtrip(t, card)

	requireFieldExists(t, card, "actions")
	actions, ok := card["actions"].([]any)
	if !ok {
		t.Fatal("actions is not []any")
	}
	if len(actions) != 2 {
		t.Errorf("got %d gate actions, want 2", len(actions))
	}

	approve := actions[0].(map[string]any)
	requireField(t, approve, "type", "Action.Submit")
	requireField(t, approve, "title", "Approve")
	requireField(t, approve, "style", "positive")

	approveData := approve["data"].(map[string]any)
	requireField(t, approveData, "action", "gate-approve")
	requireField(t, approveData, "campfire_id", msg.CampfireID)
	requireField(t, approveData, "gate_msg_id", msg.MessageID)

	reject := actions[1].(map[string]any)
	requireField(t, reject, "type", "Action.Submit")
	requireField(t, reject, "title", "Reject")
	requireField(t, reject, "style", "destructive")

	rejectData := reject["data"].(map[string]any)
	requireField(t, rejectData, "action", "gate-reject")
	requireField(t, rejectData, "campfire_id", msg.CampfireID)
	requireField(t, rejectData, "gate_msg_id", msg.MessageID)
}

// TestStatusCard checks collapse (isVisible:false) and toggle action.
func TestStatusCard(t *testing.T) {
	msg := baseMsg()
	msg.Tags = []string{"status"}
	card := BuildCard(msg)
	marshalRoundtrip(t, card)

	body := card["body"].([]any)

	// Find the toggle ActionSet and collapsed container.
	var foundToggle, foundCollapsed bool
	for _, el := range body {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		switch m["type"] {
		case "ActionSet":
			acts, _ := m["actions"].([]any)
			for _, a := range acts {
				am := a.(map[string]any)
				if am["type"] == "Action.ToggleVisibility" {
					foundToggle = true
				}
			}
		case "Container":
			if isVis, ok := m["isVisible"].(bool); ok && !isVis {
				foundCollapsed = true
			}
		}
	}

	if !foundToggle {
		t.Error("status card missing Action.ToggleVisibility")
	}
	if !foundCollapsed {
		t.Error("status card missing collapsed container (isVisible:false)")
	}
}

// TestSchemaChangeCard checks monospace fontType on payload.
func TestSchemaChangeCard(t *testing.T) {
	msg := baseMsg()
	msg.Tags = []string{"schema-change"}
	card := BuildCard(msg)
	marshalRoundtrip(t, card)

	body := card["body"].([]any)
	// Find the payload TextBlock
	found := false
	for _, el := range body {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "TextBlock" {
			if ft, ok := m["fontType"].(string); ok && ft == "Monospace" {
				found = true
			}
		}
	}
	if !found {
		t.Error("schema-change card payload missing fontType:Monospace")
	}
}

// TestFindingCard checks expandable toggle with Action.ToggleVisibility.
func TestFindingCard(t *testing.T) {
	msg := baseMsg()
	msg.Tags = []string{"finding"}
	card := BuildCard(msg)
	marshalRoundtrip(t, card)

	body := card["body"].([]any)
	var foundToggle, foundContainer bool
	for _, el := range body {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		switch m["type"] {
		case "ActionSet":
			acts, _ := m["actions"].([]any)
			for _, a := range acts {
				am := a.(map[string]any)
				if am["type"] == "Action.ToggleVisibility" {
					foundToggle = true
				}
			}
		case "Container":
			if isVis, ok := m["isVisible"].(bool); ok && !isVis {
				foundContainer = true
			}
		}
	}
	if !foundToggle {
		t.Error("finding card missing Action.ToggleVisibility")
	}
	if !foundContainer {
		t.Error("finding card missing collapsed container")
	}
}

// TestDirectiveCard checks bold (Bolder) weight on payload.
func TestDirectiveCard(t *testing.T) {
	msg := baseMsg()
	msg.Tags = []string{"directive"}
	card := BuildCard(msg)
	marshalRoundtrip(t, card)

	body := card["body"].([]any)
	found := false
	for _, el := range body {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "TextBlock" {
			if w, ok := m["weight"].(string); ok && w == "Bolder" {
				found = true
			}
		}
	}
	if !found {
		t.Error("directive card payload missing weight:Bolder")
	}
}

// TestTestFlakyCard checks FLAKY badge in the tag badges row.
func TestTestFlakyCard(t *testing.T) {
	msg := baseMsg()
	msg.Tags = []string{"test-flaky"}
	card := BuildCard(msg)
	marshalRoundtrip(t, card)

	body := card["body"].([]any)
	if len(body) < 2 {
		t.Fatal("test-flaky card body too short")
	}

	badgesRow, ok := body[1].(map[string]any)
	if !ok {
		t.Fatal("body[1] is not a map")
	}
	requireField(t, badgesRow, "type", "ColumnSet")

	cols := badgesRow["columns"].([]any)
	if len(cols) == 0 {
		t.Fatal("no badge columns in test-flaky card")
	}

	col := cols[0].(map[string]any)
	items := col["items"].([]any)
	tb := items[0].(map[string]any)

	text, _ := tb["text"].(string)
	color, _ := tb["color"].(string)

	if color != "Attention" {
		t.Errorf("test-flaky badge color = %q, want Attention", color)
	}
	// Badge text must contain FLAKY
	found := false
	for i := 0; i < len(text); i++ {
		if text[i:] >= "FLAKY" && text[i:i+5] == "FLAKY" {
			found = true
			break
		}
	}
	if !found {
		// simpler contains check
		if len(text) >= 5 {
			for j := 0; j <= len(text)-5; j++ {
				if text[j:j+5] == "FLAKY" {
					found = true
					break
				}
			}
		}
		if !found {
			t.Errorf("test-flaky badge text %q does not contain FLAKY", text)
		}
	}
}

// TestNoTagCard verifies a clean card with no tag badges and no actions.
func TestNoTagCard(t *testing.T) {
	msg := baseMsg()
	msg.Tags = nil
	card := BuildCard(msg)
	marshalRoundtrip(t, card)

	requireField(t, card, "version", "1.4")

	body := card["body"].([]any)
	// Should be: header, payload (2 elements only — no badges row)
	if len(body) != 2 {
		t.Errorf("no-tag card body has %d elements, want 2", len(body))
	}

	if _, ok := card["actions"]; ok {
		t.Error("no-tag card should not have actions")
	}
}

// TestMultiTagCard verifies first-tag color wins and all tags appear as badges.
func TestMultiTagCard(t *testing.T) {
	msg := baseMsg()
	msg.Tags = []string{"blocker", "gate", "finding"}
	card := BuildCard(msg)
	marshalRoundtrip(t, card)

	body := card["body"].([]any)
	if len(body) < 2 {
		t.Fatal("multi-tag card body too short")
	}

	badgesRow := body[1].(map[string]any)
	requireField(t, badgesRow, "type", "ColumnSet")

	cols := badgesRow["columns"].([]any)
	if len(cols) != 3 {
		t.Errorf("multi-tag badges row has %d columns, want 3", len(cols))
	}

	// First badge must have Attention color (blocker)
	firstCol := cols[0].(map[string]any)
	firstItems := firstCol["items"].([]any)
	firstTB := firstItems[0].(map[string]any)
	requireField(t, firstTB, "color", "Attention")

	// Gate actions present (gate tag)
	requireFieldExists(t, card, "actions")
	actions := card["actions"].([]any)
	if len(actions) != 2 {
		t.Errorf("multi-tag card has %d actions, want 2", len(actions))
	}
}

// TestGateActionData verifies the exact callback data shape.
func TestGateActionData(t *testing.T) {
	msg := baseMsg()
	msg.Tags = []string{"gate"}
	msg.MessageID = "msg123abc"
	msg.CampfireID = "campfire456def"
	card := BuildCard(msg)

	actions := card["actions"].([]any)
	for i, a := range actions {
		am := a.(map[string]any)
		data := am["data"].(map[string]any)
		if data["campfire_id"] != "campfire456def" {
			t.Errorf("action[%d] campfire_id = %v, want campfire456def", i, data["campfire_id"])
		}
		if data["gate_msg_id"] != "msg123abc" {
			t.Errorf("action[%d] gate_msg_id = %v, want msg123abc", i, data["gate_msg_id"])
		}
	}
}

// TestCardSizeLimit verifies large payloads are truncated.
func TestCardSizeLimit(t *testing.T) {
	msg := baseMsg()
	// Build a payload larger than 20KB
	large := make([]byte, 25*1024)
	for i := range large {
		large[i] = 'A'
	}
	msg.Payload = string(large)

	card := BuildCard(msg)
	b := marshalRoundtrip(t, card)

	if len(b) > 28*1024 {
		t.Errorf("card JSON is %d bytes, exceeds 28KB limit", len(b))
	}

	// Payload should contain truncation notice
	body := card["body"].([]any)
	found := false
	for _, el := range body {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "TextBlock" {
			text, _ := m["text"].(string)
			for j := 0; j <= len(text)-9; j++ {
				if text[j:j+9] == "truncated" {
					found = true
					break
				}
			}
		}
	}
	if !found {
		t.Error("large payload card does not contain truncation notice")
	}
}

// TestRelativeTime checks a few representative cases.
func TestRelativeTime(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{90 * time.Second, "1m ago"},
		{10 * time.Minute, "10m ago"},
		{90 * time.Minute, "1h ago"},
		{3 * time.Hour, "3h ago"},
		{25 * time.Hour, "1d ago"},
		{50 * time.Hour, "2d ago"},
	}
	for _, tc := range cases {
		got := relativeTime(time.Now().Add(-tc.d))
		if got != tc.want {
			t.Errorf("relativeTime(-%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
