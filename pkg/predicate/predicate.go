// Package predicate implements an S-expression predicate language for filtering
// campfire messages. Predicates are parsed from string syntax and evaluated
// against message records in Go (not SQL), keeping the language portable
// across transports.
//
// Grammar (S-expression style):
//
//	(and EXPR EXPR ...)
//	(or EXPR EXPR ...)
//	(not EXPR)
//	(tag "value")                  — message has this tag
//	(sender "hex-prefix")          — sender starts with prefix
//	(gt EXPR EXPR)                 — numeric greater-than
//	(lt EXPR EXPR)                 — numeric less-than
//	(gte EXPR EXPR)                — numeric greater-or-equal
//	(lte EXPR EXPR)                — numeric less-or-equal
//	(eq EXPR EXPR)                 — equality (string or numeric)
//	(field "path")                 — extract JSON field from payload (dot-separated)
//	(mul EXPR EXPR)                — multiply two numbers
//	(pow BASE EXPONENT)            — exponentiation
//	(literal VALUE)                — literal number or string
//	(timestamp)                    — message timestamp (unix nanos)
//	(payload-size)                 — byte length of the raw message payload
package predicate

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// MaxDepth is the maximum recursion depth for predicate evaluation.
// Deeply nested expressions beyond this limit return ErrDepthExceeded.
const MaxDepth = 64

// MaxChildren is the maximum number of arguments allowed in a variadic
// expression (and/or). Wide predicates beyond this limit return a parse error.
const MaxChildren = 256

// ErrDepthExceeded is returned when a predicate exceeds MaxDepth.
var ErrDepthExceeded = fmt.Errorf("predicate depth exceeds maximum (%d)", MaxDepth)

// NodeType identifies the kind of predicate node.
type NodeType int

const (
	NodeAnd         NodeType = iota // Boolean AND of children
	NodeOr                          // Boolean OR of children
	NodeNot                         // Boolean NOT of single child
	NodeTag                         // Tag match: (tag "value")
	NodeSender                      // Sender prefix match: (sender "hex")
	NodeGt                          // Greater than: (gt a b)
	NodeLt                          // Less than: (lt a b)
	NodeGte                         // Greater or equal: (gte a b)
	NodeLte                         // Less or equal: (lte a b)
	NodeEq                          // Equality: (eq a b)
	NodeField                       // Payload field: (field "path")
	NodeMul                         // Multiply: (mul a b)
	NodePow                         // Power: (pow base exp)
	NodeLiteral                     // Literal value: (literal 0.5) or "hello"
	NodeTimestamp                   // Message timestamp: (timestamp)
	NodePayloadSize                 // Raw payload byte length: (payload-size)
	NodeHasFulfillment              // Has been fulfilled: (has-fulfillment)
)

// Node is a single node in the predicate AST.
type Node struct {
	Type     NodeType
	Children []*Node
	Value    string // For NodeTag, NodeSender, NodeField, NodeLiteral
}

// MessageContext provides the data a predicate evaluates against.
type MessageContext struct {
	Tags       []string       // decoded tags
	Sender     string         // hex sender
	Timestamp  int64          // unix nanos
	Payload    map[string]any // decoded JSON payload (nil if not JSON)
	RawPayload []byte         // raw payload bytes for size-based filtering (Go-only, not serialized)
	MessageID  string         // message ID (needed for has-fulfillment)
	// FulfillmentIndex maps message IDs to true if they have been fulfilled
	// (i.e., another message exists with a "fulfills" tag and this ID as antecedent).
	// Populated by the caller before evaluation — the predicate engine doesn't
	// have access to the full message set, so callers build this index once.
	FulfillmentIndex map[string]bool
}

// EvalResult holds the result of evaluating a predicate node.
// Boolean predicates set Bool; numeric/string expressions set Num/Str.
type EvalResult struct {
	Bool  bool
	Num   float64
	Str   string
	IsNum bool
	IsStr bool
	Err   error
}

// Parse parses an S-expression predicate string into an AST.
func Parse(input string) (*Node, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty predicate")
	}
	node, rest, err := parseExpr(input)
	if err != nil {
		return nil, err
	}
	rest = strings.TrimSpace(rest)
	if rest != "" {
		return nil, fmt.Errorf("unexpected trailing input: %q", rest)
	}
	return node, nil
}

// Eval evaluates a predicate AST against a message context.
// Returns true if the message matches the predicate.
// Returns false if the predicate exceeds MaxDepth.
func Eval(node *Node, ctx *MessageContext) bool {
	r := evalDepth(node, ctx, 0)
	return r.Bool
}

// EvalSafe evaluates a predicate and returns an error if depth is exceeded.
func EvalSafe(node *Node, ctx *MessageContext) (bool, error) {
	r := evalDepth(node, ctx, 0)
	if r.Err != nil {
		return false, r.Err
	}
	return r.Bool, nil
}

// EvalNumeric evaluates a predicate AST and returns its numeric result.
// For use with weight/score expressions.
func EvalNumeric(node *Node, ctx *MessageContext) (float64, error) {
	r := evalDepth(node, ctx, 0)
	if r.Err != nil {
		return 0, r.Err
	}
	if r.IsNum {
		return r.Num, nil
	}
	return 0, fmt.Errorf("expression did not produce a numeric result")
}

// String returns the S-expression string representation of the AST.
func (n *Node) String() string {
	switch n.Type {
	case NodeAnd:
		return sexprN("and", n.Children)
	case NodeOr:
		return sexprN("or", n.Children)
	case NodeNot:
		return fmt.Sprintf("(not %s)", n.Children[0].String())
	case NodeTag:
		return fmt.Sprintf("(tag %q)", n.Value)
	case NodeSender:
		return fmt.Sprintf("(sender %q)", n.Value)
	case NodeField:
		return fmt.Sprintf("(field %q)", n.Value)
	case NodeGt:
		return sexpr2("gt", n.Children)
	case NodeLt:
		return sexpr2("lt", n.Children)
	case NodeGte:
		return sexpr2("gte", n.Children)
	case NodeLte:
		return sexpr2("lte", n.Children)
	case NodeEq:
		return sexpr2("eq", n.Children)
	case NodeMul:
		return sexpr2("mul", n.Children)
	case NodePow:
		return sexpr2("pow", n.Children)
	case NodeLiteral:
		// If it parses as a number, emit without quotes
		if _, err := strconv.ParseFloat(n.Value, 64); err == nil {
			return fmt.Sprintf("(literal %s)", n.Value)
		}
		return fmt.Sprintf("(literal %q)", n.Value)
	case NodeTimestamp:
		return "(timestamp)"
	case NodePayloadSize:
		return "(payload-size)"
	case NodeHasFulfillment:
		return "(has-fulfillment)"
	}
	return "(unknown)"
}

func sexprN(op string, children []*Node) string {
	parts := make([]string, len(children))
	for i, c := range children {
		parts[i] = c.String()
	}
	return fmt.Sprintf("(%s %s)", op, strings.Join(parts, " "))
}

func sexpr2(op string, children []*Node) string {
	return fmt.Sprintf("(%s %s %s)", op, children[0].String(), children[1].String())
}

// --- Parser ---

func parseExpr(input string) (*Node, string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, "", fmt.Errorf("unexpected end of input")
	}

	if input[0] == '(' {
		return parseSExpr(input)
	}

	// Bare string literal (quoted)
	if input[0] == '"' {
		val, rest, err := parseQuotedString(input)
		if err != nil {
			return nil, "", err
		}
		return &Node{Type: NodeLiteral, Value: val}, rest, nil
	}

	// Bare number literal
	val, rest, err := parseToken(input)
	if err != nil {
		return nil, "", err
	}
	return &Node{Type: NodeLiteral, Value: val}, rest, nil
}

func parseSExpr(input string) (*Node, string, error) {
	if input[0] != '(' {
		return nil, "", fmt.Errorf("expected '(', got %q", input[:1])
	}
	input = input[1:]
	input = strings.TrimSpace(input)

	// Read operator
	op, rest, err := parseToken(input)
	if err != nil {
		return nil, "", fmt.Errorf("reading operator: %w", err)
	}
	rest = strings.TrimSpace(rest)

	switch op {
	case "and", "or":
		return parseVariadic(op, rest)
	case "not":
		return parseUnary(op, rest)
	case "tag":
		return parseStringArg(NodeTag, rest)
	case "sender":
		return parseStringArg(NodeSender, rest)
	case "field":
		return parseStringArg(NodeField, rest)
	case "gt":
		return parseBinary(NodeGt, rest)
	case "lt":
		return parseBinary(NodeLt, rest)
	case "gte":
		return parseBinary(NodeGte, rest)
	case "lte":
		return parseBinary(NodeLte, rest)
	case "eq":
		return parseBinary(NodeEq, rest)
	case "mul":
		return parseBinary(NodeMul, rest)
	case "pow":
		return parseBinary(NodePow, rest)
	case "literal":
		return parseLiteral(rest)
	case "timestamp":
		rest = strings.TrimSpace(rest)
		if rest == "" || rest[0] != ')' {
			return nil, "", fmt.Errorf("expected ')' after timestamp")
		}
		return &Node{Type: NodeTimestamp}, rest[1:], nil
	case "payload-size":
		rest = strings.TrimSpace(rest)
		if rest == "" || rest[0] != ')' {
			return nil, "", fmt.Errorf("expected ')' after payload-size")
		}
		return &Node{Type: NodePayloadSize}, rest[1:], nil
	case "has-fulfillment":
		rest = strings.TrimSpace(rest)
		if rest == "" || rest[0] != ')' {
			return nil, "", fmt.Errorf("expected ')' after has-fulfillment")
		}
		return &Node{Type: NodeHasFulfillment}, rest[1:], nil
	default:
		return nil, "", fmt.Errorf("unknown operator: %q", op)
	}
}

func parseVariadic(op string, input string) (*Node, string, error) {
	nodeType := NodeAnd
	if op == "or" {
		nodeType = NodeOr
	}

	var children []*Node
	rest := input
	for {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return nil, "", fmt.Errorf("unexpected end of input in %s", op)
		}
		if rest[0] == ')' {
			if len(children) < 2 {
				return nil, "", fmt.Errorf("%s requires at least 2 arguments", op)
			}
			return &Node{Type: nodeType, Children: children}, rest[1:], nil
		}
		child, newRest, err := parseExpr(rest)
		if err != nil {
			return nil, "", err
		}
		children = append(children, child)
		if len(children) > MaxChildren {
			return nil, "", fmt.Errorf("%s exceeds maximum argument count (%d)", op, MaxChildren)
		}
		rest = newRest
	}
}

func parseUnary(op string, input string) (*Node, string, error) {
	child, rest, err := parseExpr(input)
	if err != nil {
		return nil, "", fmt.Errorf("parsing %s argument: %w", op, err)
	}
	rest = strings.TrimSpace(rest)
	if rest == "" || rest[0] != ')' {
		return nil, "", fmt.Errorf("expected ')' after %s", op)
	}
	return &Node{Type: NodeNot, Children: []*Node{child}}, rest[1:], nil
}

func parseStringArg(nodeType NodeType, input string) (*Node, string, error) {
	input = strings.TrimSpace(input)
	val, rest, err := parseQuotedString(input)
	if err != nil {
		return nil, "", err
	}
	rest = strings.TrimSpace(rest)
	if rest == "" || rest[0] != ')' {
		return nil, "", fmt.Errorf("expected ')' after string argument")
	}
	return &Node{Type: nodeType, Value: val}, rest[1:], nil
}

func parseBinary(nodeType NodeType, input string) (*Node, string, error) {
	left, rest, err := parseExpr(input)
	if err != nil {
		return nil, "", err
	}
	right, rest, err := parseExpr(rest)
	if err != nil {
		return nil, "", err
	}
	rest = strings.TrimSpace(rest)
	if rest == "" || rest[0] != ')' {
		return nil, "", fmt.Errorf("expected ')' after binary expression")
	}
	return &Node{Type: nodeType, Children: []*Node{left, right}}, rest[1:], nil
}

func parseLiteral(input string) (*Node, string, error) {
	input = strings.TrimSpace(input)
	var val string
	var rest string
	var err error

	if input[0] == '"' {
		val, rest, err = parseQuotedString(input)
		if err != nil {
			return nil, "", err
		}
	} else {
		val, rest, err = parseToken(input)
		if err != nil {
			return nil, "", err
		}
	}
	rest = strings.TrimSpace(rest)
	if rest == "" || rest[0] != ')' {
		return nil, "", fmt.Errorf("expected ')' after literal")
	}
	return &Node{Type: NodeLiteral, Value: val}, rest[1:], nil
}

func parseQuotedString(input string) (string, string, error) {
	if input == "" || input[0] != '"' {
		return "", "", fmt.Errorf("expected quoted string, got %q", truncate(input, 20))
	}
	var sb strings.Builder
	i := 1
	for i < len(input) {
		ch := input[i]
		if ch == '\\' && i+1 < len(input) {
			next := input[i+1]
			switch next {
			case '"', '\\':
				sb.WriteByte(next)
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			default:
				sb.WriteByte('\\')
				sb.WriteByte(next)
			}
			i += 2
			continue
		}
		if ch == '"' {
			return sb.String(), input[i+1:], nil
		}
		sb.WriteByte(ch)
		i++
	}
	return "", "", fmt.Errorf("unterminated string")
}

func parseToken(input string) (string, string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", fmt.Errorf("unexpected end of input")
	}
	i := 0
	for i < len(input) && !unicode.IsSpace(rune(input[i])) && input[i] != '(' && input[i] != ')' && input[i] != '"' {
		i++
	}
	if i == 0 {
		return "", "", fmt.Errorf("unexpected character: %q", input[:1])
	}
	return input[:i], input[i:], nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- Evaluator ---

func evalDepth(node *Node, ctx *MessageContext, depth int) EvalResult {
	if depth > MaxDepth {
		return EvalResult{Err: ErrDepthExceeded}
	}
	switch node.Type {
	case NodeAnd:
		for _, c := range node.Children {
			r := evalDepth(c, ctx, depth+1)
			if r.Err != nil {
				return r
			}
			if !r.Bool {
				return EvalResult{Bool: false}
			}
		}
		return EvalResult{Bool: true}

	case NodeOr:
		for _, c := range node.Children {
			r := evalDepth(c, ctx, depth+1)
			if r.Err != nil {
				return r
			}
			if r.Bool {
				return EvalResult{Bool: true}
			}
		}
		return EvalResult{Bool: false}

	case NodeNot:
		r := evalDepth(node.Children[0], ctx, depth+1)
		if r.Err != nil {
			return r
		}
		return EvalResult{Bool: !r.Bool}

	case NodeTag:
		target := strings.ToLower(node.Value)
		for _, t := range ctx.Tags {
			if strings.ToLower(t) == target {
				return EvalResult{Bool: true}
			}
		}
		return EvalResult{Bool: false}

	case NodeSender:
		prefix := strings.ToLower(node.Value)
		return EvalResult{Bool: strings.HasPrefix(strings.ToLower(ctx.Sender), prefix)}

	case NodeTimestamp:
		return EvalResult{Num: float64(ctx.Timestamp), IsNum: true}

	case NodePayloadSize:
		return EvalResult{Num: float64(len(ctx.RawPayload)), IsNum: true}

	case NodeHasFulfillment:
		if ctx.FulfillmentIndex != nil && ctx.MessageID != "" {
			return EvalResult{Bool: ctx.FulfillmentIndex[ctx.MessageID]}
		}
		return EvalResult{Bool: false}

	case NodeField:
		return evalField(node.Value, ctx)

	case NodeLiteral:
		return evalLiteral(node.Value)

	case NodeGt:
		left := evalDepth(node.Children[0], ctx, depth+1)
		if left.Err != nil {
			return left
		}
		right := evalDepth(node.Children[1], ctx, depth+1)
		if right.Err != nil {
			return right
		}
		return EvalResult{Bool: toNum(left) > toNum(right)}

	case NodeLt:
		left := evalDepth(node.Children[0], ctx, depth+1)
		if left.Err != nil {
			return left
		}
		right := evalDepth(node.Children[1], ctx, depth+1)
		if right.Err != nil {
			return right
		}
		return EvalResult{Bool: toNum(left) < toNum(right)}

	case NodeGte:
		left := evalDepth(node.Children[0], ctx, depth+1)
		if left.Err != nil {
			return left
		}
		right := evalDepth(node.Children[1], ctx, depth+1)
		if right.Err != nil {
			return right
		}
		return EvalResult{Bool: toNum(left) >= toNum(right)}

	case NodeLte:
		left := evalDepth(node.Children[0], ctx, depth+1)
		if left.Err != nil {
			return left
		}
		right := evalDepth(node.Children[1], ctx, depth+1)
		if right.Err != nil {
			return right
		}
		return EvalResult{Bool: toNum(left) <= toNum(right)}

	case NodeEq:
		left := evalDepth(node.Children[0], ctx, depth+1)
		if left.Err != nil {
			return left
		}
		right := evalDepth(node.Children[1], ctx, depth+1)
		if right.Err != nil {
			return right
		}
		if left.IsStr && right.IsStr {
			return EvalResult{Bool: left.Str == right.Str}
		}
		return EvalResult{Bool: toNum(left) == toNum(right)}

	case NodeMul:
		left := evalDepth(node.Children[0], ctx, depth+1)
		if left.Err != nil {
			return left
		}
		right := evalDepth(node.Children[1], ctx, depth+1)
		if right.Err != nil {
			return right
		}
		return EvalResult{Num: toNum(left) * toNum(right), IsNum: true}

	case NodePow:
		base := evalDepth(node.Children[0], ctx, depth+1)
		if base.Err != nil {
			return base
		}
		exp := evalDepth(node.Children[1], ctx, depth+1)
		if exp.Err != nil {
			return exp
		}
		return EvalResult{Num: math.Pow(toNum(base), toNum(exp)), IsNum: true}
	}

	return EvalResult{}
}

func evalField(path string, ctx *MessageContext) EvalResult {
	if ctx.Payload == nil {
		return EvalResult{}
	}

	// Strip "payload." prefix if present (field paths in predicates may include it).
	path = strings.TrimPrefix(path, "payload.")

	parts := strings.Split(path, ".")
	var current any = ctx.Payload
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return EvalResult{}
		}
		current, ok = m[part]
		if !ok {
			return EvalResult{}
		}
	}

	switch v := current.(type) {
	case float64:
		return EvalResult{Num: v, IsNum: true}
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return EvalResult{Str: v.String(), IsStr: true}
		}
		return EvalResult{Num: f, IsNum: true}
	case string:
		// Try to parse as number
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return EvalResult{Num: f, IsNum: true, Str: v, IsStr: true}
		}
		return EvalResult{Str: v, IsStr: true}
	case bool:
		return EvalResult{Bool: v}
	default:
		return EvalResult{Str: fmt.Sprint(v), IsStr: true}
	}
}

func evalLiteral(val string) EvalResult {
	if f, err := strconv.ParseFloat(val, 64); err == nil {
		return EvalResult{Num: f, IsNum: true}
	}
	return EvalResult{Str: val, IsStr: true}
}

func toNum(r EvalResult) float64 {
	if r.IsNum {
		return r.Num
	}
	if r.IsStr {
		if f, err := strconv.ParseFloat(r.Str, 64); err == nil {
			return f
		}
	}
	if r.Bool {
		return 1
	}
	return 0
}
