package projection

import (
	"testing"

	"github.com/campfire-net/campfire/pkg/predicate"
)

func TestClassify_LimitForcesAlwaysScan(t *testing.T) {
	// Even a simple incremental predicate becomes always-scan with limit > 0
	node := &predicate.Node{Type: predicate.NodeTag}
	if got := Classify(node, 10); got != ClassAlwaysScan {
		t.Errorf("Classify with limit=10: got %v, want ClassAlwaysScan", got)
	}
}

func TestClassify_NilNode(t *testing.T) {
	if got := Classify(nil, 0); got != ClassIncremental {
		t.Errorf("Classify(nil, 0): got %v, want ClassIncremental", got)
	}
}

func TestClassify_NodeTag(t *testing.T) {
	node := &predicate.Node{Type: predicate.NodeTag}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("NodeTag: got %v, want ClassIncremental", got)
	}
}

func TestClassify_NodeSender(t *testing.T) {
	node := &predicate.Node{Type: predicate.NodeSender}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("NodeSender: got %v, want ClassIncremental", got)
	}
}

func TestClassify_NodeField(t *testing.T) {
	node := &predicate.Node{Type: predicate.NodeField}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("NodeField: got %v, want ClassIncremental", got)
	}
}

func TestClassify_NodeTimestamp(t *testing.T) {
	node := &predicate.Node{Type: predicate.NodeTimestamp}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("NodeTimestamp: got %v, want ClassIncremental", got)
	}
}

func TestClassify_NodePayloadSize(t *testing.T) {
	node := &predicate.Node{Type: predicate.NodePayloadSize}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("NodePayloadSize: got %v, want ClassIncremental", got)
	}
}

func TestClassify_NodeLiteral(t *testing.T) {
	node := &predicate.Node{Type: predicate.NodeLiteral}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("NodeLiteral: got %v, want ClassIncremental", got)
	}
}

func TestClassify_NodeHasFulfillment(t *testing.T) {
	node := &predicate.Node{Type: predicate.NodeHasFulfillment}
	if got := Classify(node, 0); got != ClassAlwaysScan {
		t.Errorf("NodeHasFulfillment: got %v, want ClassAlwaysScan", got)
	}
}

func TestClassify_NodeGt(t *testing.T) {
	node := &predicate.Node{
		Type: predicate.NodeGt,
		Children: []*predicate.Node{
			{Type: predicate.NodeTimestamp},
			{Type: predicate.NodeLiteral},
		},
	}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("NodeGt with incremental children: got %v, want ClassIncremental", got)
	}
}

func TestClassify_NodeLt(t *testing.T) {
	node := &predicate.Node{
		Type: predicate.NodeLt,
		Children: []*predicate.Node{
			{Type: predicate.NodePayloadSize},
			{Type: predicate.NodeLiteral},
		},
	}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("NodeLt with incremental children: got %v, want ClassIncremental", got)
	}
}

func TestClassify_NodeGte(t *testing.T) {
	node := &predicate.Node{
		Type: predicate.NodeGte,
		Children: []*predicate.Node{
			{Type: predicate.NodeField},
			{Type: predicate.NodeLiteral},
		},
	}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("NodeGte with incremental children: got %v, want ClassIncremental", got)
	}
}

func TestClassify_NodeLte(t *testing.T) {
	node := &predicate.Node{
		Type: predicate.NodeLte,
		Children: []*predicate.Node{
			{Type: predicate.NodeTimestamp},
			{Type: predicate.NodeLiteral},
		},
	}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("NodeLte with incremental children: got %v, want ClassIncremental", got)
	}
}

func TestClassify_NodeEq(t *testing.T) {
	node := &predicate.Node{
		Type: predicate.NodeEq,
		Children: []*predicate.Node{
			{Type: predicate.NodeSender},
			{Type: predicate.NodeLiteral},
		},
	}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("NodeEq with incremental children: got %v, want ClassIncremental", got)
	}
}

func TestClassify_NodeMul(t *testing.T) {
	node := &predicate.Node{
		Type: predicate.NodeMul,
		Children: []*predicate.Node{
			{Type: predicate.NodePayloadSize},
			{Type: predicate.NodeLiteral},
		},
	}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("NodeMul with incremental children: got %v, want ClassIncremental", got)
	}
}

func TestClassify_NodePow(t *testing.T) {
	node := &predicate.Node{
		Type: predicate.NodePow,
		Children: []*predicate.Node{
			{Type: predicate.NodeLiteral},
			{Type: predicate.NodeLiteral},
		},
	}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("NodePow with incremental children: got %v, want ClassIncremental", got)
	}
}

func TestClassify_NodeNot(t *testing.T) {
	node := &predicate.Node{
		Type: predicate.NodeNot,
		Children: []*predicate.Node{
			{Type: predicate.NodeTag},
		},
	}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("NodeNot with incremental child: got %v, want ClassIncremental", got)
	}
}

func TestClassify_NodeAnd(t *testing.T) {
	node := &predicate.Node{
		Type: predicate.NodeAnd,
		Children: []*predicate.Node{
			{Type: predicate.NodeTag},
			{Type: predicate.NodeSender},
		},
	}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("NodeAnd with incremental children: got %v, want ClassIncremental", got)
	}
}

func TestClassify_NodeOr(t *testing.T) {
	node := &predicate.Node{
		Type: predicate.NodeOr,
		Children: []*predicate.Node{
			{Type: predicate.NodeTag},
			{Type: predicate.NodeSender},
		},
	}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("NodeOr with incremental children: got %v, want ClassIncremental", got)
	}
}

func TestClassify_BinaryOperatorWithAlwaysScanChild(t *testing.T) {
	// Binary operator with one always-scan child propagates to always-scan
	node := &predicate.Node{
		Type: predicate.NodeGt,
		Children: []*predicate.Node{
			{Type: predicate.NodeTimestamp},
			{Type: predicate.NodeHasFulfillment}, // always-scan child
		},
	}
	if got := Classify(node, 0); got != ClassAlwaysScan {
		t.Errorf("NodeGt with always-scan child: got %v, want ClassAlwaysScan", got)
	}
}

func TestClassify_NotWithAlwaysScanChild(t *testing.T) {
	// Not operator propagates the class of its child
	node := &predicate.Node{
		Type: predicate.NodeNot,
		Children: []*predicate.Node{
			{Type: predicate.NodeHasFulfillment}, // always-scan child
		},
	}
	if got := Classify(node, 0); got != ClassAlwaysScan {
		t.Errorf("NodeNot with always-scan child: got %v, want ClassAlwaysScan", got)
	}
}

func TestClassify_AndWithMixedChildren(t *testing.T) {
	// And with mixed children: worst class wins
	node := &predicate.Node{
		Type: predicate.NodeAnd,
		Children: []*predicate.Node{
			{Type: predicate.NodeTag},                // incremental
			{Type: predicate.NodeHasFulfillment},     // always-scan
			{Type: predicate.NodeSender},             // incremental
		},
	}
	if got := Classify(node, 0); got != ClassAlwaysScan {
		t.Errorf("NodeAnd with mixed children: got %v, want ClassAlwaysScan", got)
	}
}

func TestClassify_OrWithMixedChildren(t *testing.T) {
	// Or with mixed children: worst class wins
	node := &predicate.Node{
		Type: predicate.NodeOr,
		Children: []*predicate.Node{
			{Type: predicate.NodeTag},                // incremental
			{Type: predicate.NodeHasFulfillment},     // always-scan
		},
	}
	if got := Classify(node, 0); got != ClassAlwaysScan {
		t.Errorf("NodeOr with mixed children: got %v, want ClassAlwaysScan", got)
	}
}

func TestClassify_MalformedBinaryOperatorMissingChildren(t *testing.T) {
	// Binary operator with missing children defaults to always-scan
	node := &predicate.Node{
		Type:     predicate.NodeGt,
		Children: []*predicate.Node{{Type: predicate.NodeLiteral}}, // only 1 child
	}
	if got := Classify(node, 0); got != ClassAlwaysScan {
		t.Errorf("NodeGt with 1 child: got %v, want ClassAlwaysScan", got)
	}
}

func TestClassify_MalformedNotMissingChild(t *testing.T) {
	// Not operator with no children defaults to always-scan
	node := &predicate.Node{
		Type:     predicate.NodeNot,
		Children: []*predicate.Node{}, // no children
	}
	if got := Classify(node, 0); got != ClassAlwaysScan {
		t.Errorf("NodeNot with no children: got %v, want ClassAlwaysScan", got)
	}
}

func TestClassify_UnknownNodeType(t *testing.T) {
	// Unknown node type defaults to always-scan (safe fallback)
	node := &predicate.Node{
		Type: predicate.NodeType(999), // invalid node type
	}
	if got := Classify(node, 0); got != ClassAlwaysScan {
		t.Errorf("Unknown node type: got %v, want ClassAlwaysScan", got)
	}
}

func TestClassify_NestedIncrementalExpression(t *testing.T) {
	// Complex nested expression: (and (tag "A") (or (sender "B") (gt (timestamp) (literal 123))))
	node := &predicate.Node{
		Type: predicate.NodeAnd,
		Children: []*predicate.Node{
			{Type: predicate.NodeTag},
			{
				Type: predicate.NodeOr,
				Children: []*predicate.Node{
					{Type: predicate.NodeSender},
					{
						Type: predicate.NodeGt,
						Children: []*predicate.Node{
							{Type: predicate.NodeTimestamp},
							{Type: predicate.NodeLiteral},
						},
					},
				},
			},
		},
	}
	if got := Classify(node, 0); got != ClassIncremental {
		t.Errorf("Nested incremental expression: got %v, want ClassIncremental", got)
	}
}

func TestClassify_DeepNestedWithAlwaysScan(t *testing.T) {
	// Deep nested expression with has-fulfillment deep inside
	node := &predicate.Node{
		Type: predicate.NodeAnd,
		Children: []*predicate.Node{
			{Type: predicate.NodeTag},
			{
				Type: predicate.NodeOr,
				Children: []*predicate.Node{
					{Type: predicate.NodeSender},
					{
						Type: predicate.NodeNot,
						Children: []*predicate.Node{
							{Type: predicate.NodeHasFulfillment}, // deep inside
						},
					},
				},
			},
		},
	}
	if got := Classify(node, 0); got != ClassAlwaysScan {
		t.Errorf("Deep nested with has-fulfillment: got %v, want ClassAlwaysScan", got)
	}
}
