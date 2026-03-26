package naming

import (
	"testing"

	"github.com/campfire-net/campfire/pkg/predicate"
)

func TestValidatePredicate_Safe(t *testing.T) {
	safe := []string{
		`(tag "post")`,
		`(not (tag "retract"))`,
		`(and (tag "post") (not (tag "retract")))`,
		`(or (tag "post") (tag "reply"))`,
		`(and (tag "post") (or (tag "topic:ai") (tag "topic:ml")))`,
	}
	for _, expr := range safe {
		if err := ValidatePredicate(expr); err != nil {
			t.Errorf("ValidatePredicate(%q) = %v, want nil", expr, err)
		}
	}
}

func TestValidatePredicate_Unsafe(t *testing.T) {
	unsafe := []struct {
		expr  string
		errRe string
	}{
		{`(field "payload.secret")`, "field"},
		{`(sender "abcd")`, "sender"},
		{`(timestamp)`, "timestamp"},
		{`(payload-size)`, "payload-size"},
		{`(and (tag "post") (field "payload.x"))`, "field"},
		{`(gt (literal 1) (literal 2))`, "not allowed"},
	}
	for _, tt := range unsafe {
		t.Run(tt.expr, func(t *testing.T) {
			err := ValidatePredicate(tt.expr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !containsCI(err.Error(), tt.errRe) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.errRe)
			}
		})
	}
}

func TestValidatePredicate_NodeBudget(t *testing.T) {
	// Build a predicate with >32 nodes: (and (tag "a") (tag "b") ... (tag "z") ...)
	// 33 tag nodes + 1 and node = 34 nodes
	expr := "(and"
	for i := 0; i < 33; i++ {
		expr += ` (tag "x")`
	}
	expr += ")"

	err := ValidatePredicate(expr)
	if err == nil {
		t.Fatal("expected node budget exceeded error")
	}
	if !containsCI(err.Error(), "node count") {
		t.Errorf("error %q does not mention node count", err.Error())
	}
}

func TestEvalPredicateSafe(t *testing.T) {
	ctx := &predicate.MessageContext{
		Tags: []string{"post", "topic:ai"},
	}

	match, err := EvalPredicateSafe(`(and (tag "post") (not (tag "retract")))`, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !match {
		t.Error("expected match")
	}

	match, err = EvalPredicateSafe(`(tag "retract")`, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if match {
		t.Error("expected no match")
	}
}

func TestEvalPredicateSafe_RejectsUnsafe(t *testing.T) {
	ctx := &predicate.MessageContext{
		Tags: []string{"post"},
	}

	_, err := EvalPredicateSafe(`(sender "abcd")`, ctx)
	if err == nil {
		t.Fatal("expected error for unsafe predicate")
	}
}
