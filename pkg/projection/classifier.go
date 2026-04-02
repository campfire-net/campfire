package projection

import "github.com/campfire-net/campfire/pkg/predicate"

// Class represents the incrementalizability class of a filter expression.
type Class int

const (
	ClassIncremental Class = iota // O(1) on new message arrival
	ClassAlwaysScan               // must scan all messages in history
)

// Classify determines the incrementalizability class of a filter expression AST.
// Returns ClassAlwaysScan if limit > 0 (bounded result sets require full scan).
// Returns ClassIncremental if the predicate can be evaluated incrementally on new messages.
// Returns ClassAlwaysScan if the predicate contains has-fulfillment or unknown node types.
func Classify(node *predicate.Node, limit int) Class {
	if limit > 0 {
		return ClassAlwaysScan
	}
	return classifyNode(node)
}

func classifyNode(node *predicate.Node) Class {
	if node == nil {
		return ClassIncremental
	}

	switch node.Type {
	case predicate.NodeHasFulfillment:
		// has-fulfillment requires scanning all messages to check fulfillment chains
		return ClassAlwaysScan

	case predicate.NodeTag, predicate.NodeSender, predicate.NodeField,
		predicate.NodeTimestamp, predicate.NodePayloadSize, predicate.NodeLiteral:
		// These nodes can be evaluated incrementally on each new message
		return ClassIncremental

	case predicate.NodeGt, predicate.NodeLt, predicate.NodeGte, predicate.NodeLte,
		predicate.NodeEq, predicate.NodeMul, predicate.NodePow:
		// Binary operators: class is worst of both children
		if len(node.Children) < 2 {
			return ClassAlwaysScan // malformed node
		}
		return worst(classifyNode(node.Children[0]), classifyNode(node.Children[1]))

	case predicate.NodeNot:
		// Unary operator: class is same as child
		if len(node.Children) < 1 {
			return ClassAlwaysScan // malformed node
		}
		return classifyNode(node.Children[0])

	case predicate.NodeAnd, predicate.NodeOr:
		// Logical operators: class is worst of all children
		c := ClassIncremental
		for _, child := range node.Children {
			c = worst(c, classifyNode(child))
		}
		return c

	default:
		// Unknown node type: safe fallback to always-scan
		return ClassAlwaysScan
	}
}

func worst(a, b Class) Class {
	if a > b {
		return a
	}
	return b
}
