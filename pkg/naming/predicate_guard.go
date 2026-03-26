package naming

import (
	"fmt"
	"time"

	"github.com/campfire-net/campfire/pkg/predicate"
)

// MaxPredicateNodes is the total node count budget for convention-provided predicates.
const MaxPredicateNodes = 32

// PredicateEvalTimeout is the per-message evaluation timeout.
const PredicateEvalTimeout = 1 * time.Millisecond

// ValidatePredicate checks that a predicate expression uses only the safe operator
// subset allowed for convention-provided predicates: tag, not, and, or.
// Returns an error if the predicate contains disallowed operators or exceeds
// the node count budget.
func ValidatePredicate(expr string) error {
	node, err := predicate.Parse(expr)
	if err != nil {
		return fmt.Errorf("parse predicate: %w", err)
	}
	count := 0
	return validateNode(node, &count)
}

// validateNode recursively checks a predicate node for safety.
func validateNode(node *predicate.Node, count *int) error {
	*count++
	if *count > MaxPredicateNodes {
		return fmt.Errorf("predicate exceeds maximum node count of %d", MaxPredicateNodes)
	}

	switch node.Type {
	case predicate.NodeTag:
		// Safe: (tag "value")
		return nil
	case predicate.NodeNot:
		// Safe: (not EXPR)
		if len(node.Children) != 1 {
			return fmt.Errorf("not requires exactly 1 child")
		}
		return validateNode(node.Children[0], count)
	case predicate.NodeAnd, predicate.NodeOr:
		// Safe: (and EXPR EXPR ...) / (or EXPR EXPR ...)
		for _, child := range node.Children {
			if err := validateNode(child, count); err != nil {
				return err
			}
		}
		return nil
	case predicate.NodeField:
		return fmt.Errorf("operator 'field' not allowed in convention predicates")
	case predicate.NodeSender:
		return fmt.Errorf("operator 'sender' not allowed in convention predicates")
	case predicate.NodeTimestamp:
		return fmt.Errorf("operator 'timestamp' not allowed in convention predicates")
	case predicate.NodePayloadSize:
		return fmt.Errorf("operator 'payload-size' not allowed in convention predicates")
	default:
		return fmt.Errorf("operator type %d not allowed in convention predicates", node.Type)
	}
}

// EvalPredicateSafe parses, validates, and evaluates a convention predicate.
// Returns an error if the predicate is invalid or evaluation times out.
func EvalPredicateSafe(expr string, ctx *predicate.MessageContext) (bool, error) {
	if err := ValidatePredicate(expr); err != nil {
		return false, err
	}

	node, err := predicate.Parse(expr)
	if err != nil {
		return false, err
	}

	// Evaluate with timeout via channel
	type result struct {
		match bool
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		m, e := predicate.EvalSafe(node, ctx)
		ch <- result{m, e}
	}()

	select {
	case r := <-ch:
		return r.match, r.err
	case <-time.After(PredicateEvalTimeout):
		return false, fmt.Errorf("predicate evaluation timed out after %v", PredicateEvalTimeout)
	}
}
