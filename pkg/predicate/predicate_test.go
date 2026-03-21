package predicate

import (
	"fmt"
	"math"
	"testing"
)

func TestParseAndEval_TagMatch(t *testing.T) {
	node, err := Parse(`(tag "memory:standing")`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{
		Tags: []string{"memory:standing", "other"},
	}
	if !Eval(node, ctx) {
		t.Error("expected match for memory:standing tag")
	}
	ctx.Tags = []string{"other"}
	if Eval(node, ctx) {
		t.Error("expected no match without memory:standing tag")
	}
}

func TestParseAndEval_TagMatchCaseInsensitive(t *testing.T) {
	node, err := Parse(`(tag "Memory:Standing")`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{Tags: []string{"memory:standing"}}
	if !Eval(node, ctx) {
		t.Error("tag match should be case-insensitive")
	}
}

func TestParseAndEval_SenderMatch(t *testing.T) {
	node, err := Parse(`(sender "abcd12")`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{Sender: "abcd1234567890"}
	if !Eval(node, ctx) {
		t.Error("expected sender prefix match")
	}
	ctx.Sender = "eeee1234567890"
	if Eval(node, ctx) {
		t.Error("expected no match for different sender")
	}
}

func TestParseAndEval_TimestampGt(t *testing.T) {
	node, err := Parse(`(gt (timestamp) (literal 1000000000))`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{Timestamp: 2000000000}
	if !Eval(node, ctx) {
		t.Error("expected timestamp > 1000000000")
	}
	ctx.Timestamp = 500000000
	if Eval(node, ctx) {
		t.Error("expected no match for small timestamp")
	}
}

func TestParseAndEval_TimestampBetween(t *testing.T) {
	node, err := Parse(`(and (gt (timestamp) (literal 100)) (lt (timestamp) (literal 300)))`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{Timestamp: 200}
	if !Eval(node, ctx) {
		t.Error("expected 200 between 100 and 300")
	}
	ctx.Timestamp = 50
	if Eval(node, ctx) {
		t.Error("50 should not be between 100 and 300")
	}
	ctx.Timestamp = 400
	if Eval(node, ctx) {
		t.Error("400 should not be between 100 and 300")
	}
}

func TestParseAndEval_PayloadField(t *testing.T) {
	node, err := Parse(`(eq (field "payload.confidence") (literal "high"))`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{
		Payload: map[string]any{"confidence": "high"},
	}
	if !Eval(node, ctx) {
		t.Error("expected match for confidence=high")
	}
	ctx.Payload = map[string]any{"confidence": "low"}
	if Eval(node, ctx) {
		t.Error("expected no match for confidence=low")
	}
}

func TestParseAndEval_PayloadFieldNumeric(t *testing.T) {
	node, err := Parse(`(gt (field "payload.confidence") (literal 0.5))`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{
		Payload: map[string]any{"confidence": 0.8},
	}
	if !Eval(node, ctx) {
		t.Error("expected 0.8 > 0.5")
	}
	ctx.Payload = map[string]any{"confidence": 0.3}
	if Eval(node, ctx) {
		t.Error("expected 0.3 not > 0.5")
	}
}

func TestParseAndEval_NestedPayloadField(t *testing.T) {
	node, err := Parse(`(eq (field "payload.meta.category") (literal "identity"))`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{
		Payload: map[string]any{
			"meta": map[string]any{
				"category": "identity",
			},
		},
	}
	if !Eval(node, ctx) {
		t.Error("expected nested field match")
	}
}

func TestParseAndEval_BooleanComposition(t *testing.T) {
	node, err := Parse(`(or (tag "memory:standing") (tag "memory:anchor"))`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{Tags: []string{"memory:standing"}}
	if !Eval(node, ctx) {
		t.Error("expected match for memory:standing in OR")
	}
	ctx.Tags = []string{"memory:anchor"}
	if !Eval(node, ctx) {
		t.Error("expected match for memory:anchor in OR")
	}
	ctx.Tags = []string{"other"}
	if Eval(node, ctx) {
		t.Error("expected no match for unrelated tag in OR")
	}
}

func TestParseAndEval_NotOperator(t *testing.T) {
	node, err := Parse(`(not (tag "deprecated"))`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{Tags: []string{"active"}}
	if !Eval(node, ctx) {
		t.Error("expected match when tag is absent")
	}
	ctx.Tags = []string{"deprecated"}
	if Eval(node, ctx) {
		t.Error("expected no match when tag is present")
	}
}

func TestParseAndEval_ComplexBoolean(t *testing.T) {
	// (and (tag "memory:standing") (gt (field "payload.confidence") 0.5))
	node, err := Parse(`(and (tag "memory:standing") (gt (field "payload.confidence") (literal 0.5)))`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{
		Tags:    []string{"memory:standing"},
		Payload: map[string]any{"confidence": 0.8},
	}
	if !Eval(node, ctx) {
		t.Error("expected match for standing + high confidence")
	}
	ctx.Payload = map[string]any{"confidence": 0.3}
	if Eval(node, ctx) {
		t.Error("expected no match for standing + low confidence")
	}
	ctx.Tags = []string{"other"}
	ctx.Payload = map[string]any{"confidence": 0.8}
	if Eval(node, ctx) {
		t.Error("expected no match for wrong tag + high confidence")
	}
}

func TestParseAndEval_NumericComputation(t *testing.T) {
	// Weight expression: confidence * pow(0.9, sessions_since)
	node, err := Parse(`(gt (mul (field "payload.confidence") (pow (literal 0.9) (field "payload.sessions_since"))) (literal 0.10))`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	// confidence=0.8, sessions_since=2 → 0.8 * 0.9^2 = 0.8 * 0.81 = 0.648 > 0.10
	ctx := &MessageContext{
		Tags:    []string{"memory:standing"},
		Payload: map[string]any{"confidence": 0.8, "sessions_since": float64(2)},
	}
	if !Eval(node, ctx) {
		t.Error("expected 0.648 > 0.10")
	}

	// confidence=0.1, sessions_since=50 → 0.1 * 0.9^50 ≈ 0.1 * 0.00515 ≈ 0.000515 < 0.10
	ctx.Payload = map[string]any{"confidence": 0.1, "sessions_since": float64(50)}
	if Eval(node, ctx) {
		t.Error("expected very decayed value < 0.10")
	}
}

func TestEvalNumeric(t *testing.T) {
	node, err := Parse(`(mul (field "payload.confidence") (pow (literal 0.9) (field "payload.sessions_since")))`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{
		Payload: map[string]any{"confidence": 0.8, "sessions_since": float64(2)},
	}
	val, err := EvalNumeric(node, ctx)
	if err != nil {
		t.Fatalf("eval numeric error: %v", err)
	}
	expected := 0.8 * math.Pow(0.9, 2)
	if math.Abs(val-expected) > 0.001 {
		t.Errorf("expected ~%f, got %f", expected, val)
	}
}

func TestParseError_Empty(t *testing.T) {
	_, err := Parse("")
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestParseError_UnknownOp(t *testing.T) {
	_, err := Parse(`(foobar "test")`)
	if err == nil {
		t.Error("expected error for unknown operator")
	}
}

func TestParseError_UnterminatedString(t *testing.T) {
	_, err := Parse(`(tag "unterminated)`)
	if err == nil {
		t.Error("expected error for unterminated string")
	}
}

func TestParseError_TrailingInput(t *testing.T) {
	_, err := Parse(`(tag "a") extra`)
	if err == nil {
		t.Error("expected error for trailing input")
	}
}

func TestParseError_AndRequiresTwo(t *testing.T) {
	_, err := Parse(`(and (tag "a"))`)
	if err == nil {
		t.Error("expected error: and requires at least 2 arguments")
	}
}

func TestNodeString_RoundTrip(t *testing.T) {
	cases := []string{
		`(tag "memory:standing")`,
		`(sender "abcd12")`,
		`(and (tag "memory:standing") (gt (field "payload.confidence") (literal 0.5)))`,
		`(or (tag "memory:standing") (tag "memory:anchor"))`,
		`(not (tag "deprecated"))`,
		`(gt (mul (field "payload.confidence") (pow (literal 0.9) (field "payload.sessions_since"))) (literal 0.1))`,
		`(timestamp)`,
		`(payload-size)`,
		`(lt (payload-size) (literal 65536))`,
	}
	for _, c := range cases {
		node, err := Parse(c)
		if err != nil {
			t.Errorf("parse %q: %v", c, err)
			continue
		}
		s := node.String()
		node2, err := Parse(s)
		if err != nil {
			t.Errorf("re-parse %q (from %q): %v", s, c, err)
			continue
		}
		s2 := node2.String()
		if s != s2 {
			t.Errorf("round-trip mismatch: %q -> %q -> %q", c, s, s2)
		}
	}
}

func TestParseAndEval_GteAndLte(t *testing.T) {
	node, err := Parse(`(gte (literal 5) (literal 5))`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if !Eval(node, &MessageContext{}) {
		t.Error("5 >= 5 should be true")
	}

	node, err = Parse(`(lte (literal 3) (literal 5))`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if !Eval(node, &MessageContext{}) {
		t.Error("3 <= 5 should be true")
	}
}

func TestEval_NilPayload(t *testing.T) {
	node, err := Parse(`(field "payload.missing")`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{} // nil payload
	r := evalDepth(node, ctx, 0)
	if r.IsNum || r.IsStr || r.Bool {
		t.Error("field on nil payload should return zero result")
	}
}

func TestEval_MissingField(t *testing.T) {
	node, err := Parse(`(gt (field "payload.nonexistent") (literal 0))`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{
		Payload: map[string]any{"other": 1.0},
	}
	// Missing field yields 0, so 0 > 0 = false
	if Eval(node, ctx) {
		t.Error("missing field should yield 0, so 0 > 0 = false")
	}
}

func TestParse_BareNumbers(t *testing.T) {
	// Numbers used directly as arguments (not wrapped in literal)
	node, err := Parse(`(gt (literal 5) 3)`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if !Eval(node, &MessageContext{}) {
		t.Error("5 > 3 should be true")
	}
}

func TestParse_MultipleAndChildren(t *testing.T) {
	node, err := Parse(`(and (tag "a") (tag "b") (tag "c"))`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{Tags: []string{"a", "b", "c"}}
	if !Eval(node, ctx) {
		t.Error("expected all three tags to match")
	}
	ctx.Tags = []string{"a", "b"}
	if Eval(node, ctx) {
		t.Error("expected failure when c is missing")
	}
}

func TestParse_EscapedStrings(t *testing.T) {
	node, err := Parse(`(tag "has\"quote")`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{Tags: []string{`has"quote`}}
	if !Eval(node, ctx) {
		t.Error("expected match for escaped quote tag")
	}
}

func TestEval_DepthLimit(t *testing.T) {
	// Build a deeply nested expression: (not (not (not ... (tag "x") ...)))
	expr := `(tag "x")`
	for i := 0; i < MaxDepth+5; i++ {
		expr = fmt.Sprintf("(not %s)", expr)
	}
	node, err := Parse(expr)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{Tags: []string{"x"}}
	_, err = EvalSafe(node, ctx)
	if err != ErrDepthExceeded {
		t.Errorf("expected ErrDepthExceeded, got %v", err)
	}
}

func TestEval_AtMaxDepth(t *testing.T) {
	// Build expression at exactly MaxDepth — should succeed
	expr := `(tag "x")`
	for i := 0; i < MaxDepth-1; i++ {
		expr = fmt.Sprintf("(not %s)", expr)
	}
	node, err := Parse(expr)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := &MessageContext{Tags: []string{"x"}}
	_, err = EvalSafe(node, ctx)
	if err != nil {
		t.Errorf("unexpected error at max depth: %v", err)
	}
	// Don't check bool value — odd/even nesting of not will flip it
}

func TestParseAndEval_PayloadSize(t *testing.T) {
	node, err := Parse(`(lt (payload-size) (literal 65536))`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	// 100-byte payload: 100 < 65536 → true
	smallPayload := make([]byte, 100)
	ctx := &MessageContext{RawPayload: smallPayload}
	if !Eval(node, ctx) {
		t.Error("expected (lt (payload-size) 65536) to be true for 100-byte payload")
	}

	// 70000-byte payload: 70000 < 65536 → false
	largePayload := make([]byte, 70000)
	ctx.RawPayload = largePayload
	if Eval(node, ctx) {
		t.Error("expected (lt (payload-size) 65536) to be false for 70000-byte payload")
	}
}

func TestParseAndEval_PayloadSizeNilPayload(t *testing.T) {
	node, err := Parse(`(lt (payload-size) (literal 65536))`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	// No RawPayload set: size 0 < 65536 → true
	ctx := &MessageContext{}
	if !Eval(node, ctx) {
		t.Error("expected (lt (payload-size) 65536) to be true when RawPayload is nil")
	}
}

func TestNodeString_PayloadSize(t *testing.T) {
	node, err := Parse(`(payload-size)`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if node.String() != "(payload-size)" {
		t.Errorf("expected (payload-size), got %q", node.String())
	}
}
