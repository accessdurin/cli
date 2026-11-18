package jsonc

import (
	"math"
	"strconv"
)

// Port of lib/esm/impl/parser.js (visit, parse, parseTree,
// findNodeAtLocation).

// ParseErrorCode identifies a parse error; values match upstream's
// ParseErrorCode enum.
type ParseErrorCode int

// Parse error codes.
const (
	InvalidSymbol          ParseErrorCode = 1
	InvalidNumberFormat    ParseErrorCode = 2
	PropertyNameExpected   ParseErrorCode = 3
	ValueExpected          ParseErrorCode = 4
	ColonExpected          ParseErrorCode = 5
	CommaExpected          ParseErrorCode = 6
	CloseBraceExpected     ParseErrorCode = 7
	CloseBracketExpected   ParseErrorCode = 8
	EndOfFileExpected      ParseErrorCode = 9
	InvalidCommentToken    ParseErrorCode = 10
	UnexpectedEndOfComment ParseErrorCode = 11
	UnexpectedEndOfString  ParseErrorCode = 12
	UnexpectedEndOfNumber  ParseErrorCode = 13
	InvalidUnicode         ParseErrorCode = 14
	InvalidEscapeCharacter ParseErrorCode = 15
	InvalidCharacter       ParseErrorCode = 16
)

var parseErrorNames = map[ParseErrorCode]string{
	InvalidSymbol:          "InvalidSymbol",
	InvalidNumberFormat:    "InvalidNumberFormat",
	PropertyNameExpected:   "PropertyNameExpected",
	ValueExpected:          "ValueExpected",
	ColonExpected:          "ColonExpected",
	CommaExpected:          "CommaExpected",
	CloseBraceExpected:     "CloseBraceExpected",
	CloseBracketExpected:   "CloseBracketExpected",
	EndOfFileExpected:      "EndOfFileExpected",
	InvalidCommentToken:    "InvalidCommentToken",
	UnexpectedEndOfComment: "UnexpectedEndOfComment",
	UnexpectedEndOfString:  "UnexpectedEndOfString",
	UnexpectedEndOfNumber:  "UnexpectedEndOfNumber",
	InvalidUnicode:         "InvalidUnicode",
	InvalidEscapeCharacter: "InvalidEscapeCharacter",
	InvalidCharacter:       "InvalidCharacter",
}

// String returns the name upstream's printParseErrorCode uses.
func (c ParseErrorCode) String() string {
	if name, ok := parseErrorNames[c]; ok {
		return name
	}
	return "<unknown ParseErrorCode>"
}

// ParseError is one recoverable syntax error. Offset and Length are byte
// offsets into the parsed text.
type ParseError struct {
	Code   ParseErrorCode
	Offset int
	Length int
}

// ParseOptions mirrors upstream's ParseOptions. The zero value is upstream's
// default (comments allowed, trailing commas and empty content rejected).
type ParseOptions struct {
	DisallowComments   bool
	AllowTrailingComma bool
	AllowEmptyContent  bool
}

// NodeType is the type of a Node.
type NodeType string

// Node types.
const (
	NodeObject   NodeType = "object"
	NodeArray    NodeType = "array"
	NodeProperty NodeType = "property"
	NodeString   NodeType = "string"
	NodeNumber   NodeType = "number"
	NodeBoolean  NodeType = "boolean"
	NodeNull     NodeType = "null"
)

// Node is a node of the tree ParseTree returns. Offsets are in bytes.
// Length is -1 for a container or property that never ended (truncated
// input). ColonOffset is set on property nodes that have a colon and is -1
// otherwise. Value holds the literal value of string, number, boolean and
// null nodes (property keys are string nodes).
type Node struct {
	Type        NodeType
	Value       any
	Offset      int
	Length      int
	ColonOffset int
	Parent      *Node
	Children    []*Node
}

type visitor struct {
	onObjectBegin    func(offset, length int)
	onObjectProperty func(name string, offset, length int)
	onObjectEnd      func(offset, length int)
	onArrayBegin     func(offset, length int)
	onArrayEnd       func(offset, length int)
	onLiteralValue   func(value any, offset, length int)
	onSeparator      func(sep byte, offset, length int)
	onError          func(code ParseErrorCode, offset, length int)
}

// visit ports upstream's visit. Callback suppression (a begin callback
// returning false) and JSON paths are not ported; no caller here uses them.
func visit(text string, v visitor, options ParseOptions) {
	sc := newScanner(text)
	off := func() (int, int) { return sc.tokenOffset, sc.tokenLength() }
	onError := func(code ParseErrorCode) {
		if v.onError != nil {
			o, l := off()
			v.onError(code, o, l)
		}
	}
	onSeparator := func(sep byte) {
		if v.onSeparator != nil {
			o, l := off()
			v.onSeparator(sep, o, l)
		}
	}
	onLiteral := func(value any) {
		if v.onLiteralValue != nil {
			o, l := off()
			v.onLiteralValue(value, o, l)
		}
	}
	call := func(f func(int, int)) {
		if f != nil {
			o, l := off()
			f(o, l)
		}
	}

	var handleError func(code ParseErrorCode, skipUntilAfter, skipUntil []syntaxKind)
	scanNext := func() syntaxKind {
		for {
			token := sc.scan()
			switch sc.err {
			case scanErrInvalidUnicode:
				handleError(InvalidUnicode, nil, nil)
			case scanErrInvalidEscapeCharacter:
				handleError(InvalidEscapeCharacter, nil, nil)
			case scanErrUnexpectedEndOfNumber:
				handleError(UnexpectedEndOfNumber, nil, nil)
			case scanErrUnexpectedEndOfComment:
				if !options.DisallowComments {
					handleError(UnexpectedEndOfComment, nil, nil)
				}
			case scanErrUnexpectedEndOfString:
				handleError(UnexpectedEndOfString, nil, nil)
			case scanErrInvalidCharacter:
				handleError(InvalidCharacter, nil, nil)
			}
			switch token {
			case tokLineComment, tokBlockComment:
				if options.DisallowComments {
					handleError(InvalidCommentToken, nil, nil)
				}
			case tokUnknown:
				handleError(InvalidSymbol, nil, nil)
			case tokTrivia, tokLineBreak:
			default:
				return token
			}
		}
	}
	contains := func(list []syntaxKind, t syntaxKind) bool {
		for _, k := range list {
			if k == t {
				return true
			}
		}
		return false
	}
	handleError = func(code ParseErrorCode, skipUntilAfter, skipUntil []syntaxKind) {
		onError(code)
		if len(skipUntilAfter)+len(skipUntil) > 0 {
			token := sc.token
			for token != tokEOF {
				if contains(skipUntilAfter, token) {
					scanNext()
					break
				} else if contains(skipUntil, token) {
					break
				}
				token = scanNext()
			}
		}
	}

	parseString := func(isValue bool) bool {
		value := sc.value
		if isValue {
			onLiteral(value)
		} else if v.onObjectProperty != nil {
			o, l := off()
			v.onObjectProperty(value, o, l)
		}
		scanNext()
		return true
	}
	parseLiteral := func() bool {
		switch sc.token {
		case tokNumber:
			value, err := strconv.ParseFloat(sc.value, 64)
			if err != nil && !math.IsInf(value, 0) {
				handleError(InvalidNumberFormat, nil, nil)
				value = 0
			}
			onLiteral(value)
		case tokNull:
			onLiteral(nil)
		case tokTrue:
			onLiteral(true)
		case tokFalse:
			onLiteral(false)
		default:
			return false
		}
		scanNext()
		return true
	}
	braceOrComma := []syntaxKind{tokCloseBrace, tokComma}
	bracketOrComma := []syntaxKind{tokCloseBracket, tokComma}

	var parseValue func() bool
	parseProperty := func() bool {
		if sc.token != tokString {
			handleError(PropertyNameExpected, nil, braceOrComma)
			return false
		}
		parseString(false)
		if sc.token == tokColon {
			onSeparator(':')
			scanNext() // consume colon
			if !parseValue() {
				handleError(ValueExpected, nil, braceOrComma)
			}
		} else {
			handleError(ColonExpected, nil, braceOrComma)
		}
		return true
	}
	parseObject := func() bool {
		call(v.onObjectBegin)
		scanNext() // consume open brace
		needsComma := false
		for sc.token != tokCloseBrace && sc.token != tokEOF {
			if sc.token == tokComma {
				if !needsComma {
					handleError(ValueExpected, nil, nil)
				}
				onSeparator(',')
				scanNext() // consume comma
				if sc.token == tokCloseBrace && options.AllowTrailingComma {
					break
				}
			} else if needsComma {
				handleError(CommaExpected, nil, nil)
			}
			if !parseProperty() {
				handleError(ValueExpected, nil, braceOrComma)
			}
			needsComma = true
		}
		call(v.onObjectEnd)
		if sc.token != tokCloseBrace {
			handleError(CloseBraceExpected, []syntaxKind{tokCloseBrace}, nil)
		} else {
			scanNext() // consume close brace
		}
		return true
	}
	parseArray := func() bool {
		call(v.onArrayBegin)
		scanNext() // consume open bracket
		needsComma := false
		for sc.token != tokCloseBracket && sc.token != tokEOF {
			if sc.token == tokComma {
				if !needsComma {
					handleError(ValueExpected, nil, nil)
				}
				onSeparator(',')
				scanNext() // consume comma
				if sc.token == tokCloseBracket && options.AllowTrailingComma {
					break
				}
			} else if needsComma {
				handleError(CommaExpected, nil, nil)
			}
			if !parseValue() {
				handleError(ValueExpected, nil, bracketOrComma)
			}
			needsComma = true
		}
		call(v.onArrayEnd)
		if sc.token != tokCloseBracket {
			handleError(CloseBracketExpected, []syntaxKind{tokCloseBracket}, nil)
		} else {
			scanNext() // consume close bracket
		}
		return true
	}
	parseValue = func() bool {
		switch sc.token {
		case tokOpenBracket:
			return parseArray()
		case tokOpenBrace:
			return parseObject()
		case tokString:
			return parseString(true)
		default:
			return parseLiteral()
		}
	}

	scanNext()
	if sc.token == tokEOF {
		if options.AllowEmptyContent {
			return
		}
		handleError(ValueExpected, nil, nil)
		return
	}
	if !parseValue() {
		handleError(ValueExpected, nil, nil)
		return
	}
	if sc.token != tokEOF {
		handleError(EndOfFileExpected, nil, nil)
	}
}

// Parse parses JSONC text into a value (see the package documentation for
// the Go types used) and returns it with every recoverable error found. Like
// upstream it is fault tolerant: callers must check the errors to know
// whether the text was valid. Empty input yields nil plus a ValueExpected
// error (upstream returns undefined).
//
// Object semantics follow assignment on a JavaScript object: for duplicate
// keys the last value wins and the key keeps its first position. A
// "__proto__" key is emulated as far as which own keys result: it never
// becomes an own key while Object.prototype's __proto__ setter is reachable
// (assigning an object, array or null replaces the prototype; other values
// are ignored), and becomes an ordinary key once a null prototype has cut
// that setter off. Properties inherited through such a prototype are not
// visible in the result.
func Parse(text string, options ParseOptions) (any, []ParseError) {
	var errs []ParseError
	type frame struct {
		obj    *Object
		arr    []any
		isArr  bool
		key    string
		hasKey bool
	}
	root := &frame{isArr: true}
	stack := []*frame{root}
	var currentProperty string
	hasProperty := false
	top := func() *frame { return stack[len(stack)-1] }
	assign := func(f *frame, key string, hasKey bool, value any) {
		if f.isArr {
			f.arr = append(f.arr, value)
			return
		}
		if !hasKey {
			return
		}
		// While Object.prototype's __proto__ setter is reachable through
		// the prototype chain, assigning "__proto__" replaces the
		// prototype (objects, arrays, null) or does nothing (other values).
		if key == "__proto__" && !f.obj.protoDetached {
			switch x := value.(type) {
			case nil:
				f.obj.protoDetached = true
			case *Object:
				f.obj.protoDetached = x.protoDetached
			case []any:
				f.obj.protoDetached = false // Array.prototype inherits from Object.prototype
			}
			return
		}
		f.obj.Set(key, value)
	}
	begin := func(isArr bool) {
		f := &frame{isArr: isArr, key: currentProperty, hasKey: hasProperty}
		if isArr {
			f.arr = []any{}
		} else {
			f.obj = NewObject()
		}
		stack = append(stack, f)
		hasProperty = false
	}
	end := func(int, int) {
		f := top()
		stack = stack[:len(stack)-1]
		if f.isArr {
			assign(top(), f.key, f.hasKey, f.arr)
		} else {
			assign(top(), f.key, f.hasKey, f.obj)
		}
	}
	visit(text, visitor{
		onObjectBegin: func(int, int) { begin(false) },
		onObjectProperty: func(name string, _, _ int) {
			currentProperty, hasProperty = name, true
		},
		onObjectEnd:  end,
		onArrayBegin: func(int, int) { begin(true) },
		onArrayEnd:   end,
		onLiteralValue: func(value any, _, _ int) {
			assign(top(), currentProperty, hasProperty, value)
		},
		onError: func(code ParseErrorCode, offset, length int) {
			errs = append(errs, ParseError{code, offset, length})
		},
	}, options)
	if len(root.arr) == 0 {
		return nil, errs
	}
	return root.arr[0], errs
}

// ParseTree parses JSONC text into a Node tree, returning nil for empty
// input. Like Parse it is fault tolerant.
func ParseTree(text string, options ParseOptions) (*Node, []ParseError) {
	var errs []ParseError
	current := &Node{Type: NodeArray, Offset: -1, Length: -1, ColonOffset: -1} // artificial root
	ensurePropertyComplete := func(endOffset int) {
		if current.Type == NodeProperty {
			current.Length = endOffset - current.Offset
			current = current.Parent
		}
	}
	onValue := func(n *Node) *Node {
		current.Children = append(current.Children, n)
		return n
	}
	container := func(t NodeType) func(int, int) {
		return func(offset, _ int) {
			current = onValue(&Node{Type: t, Offset: offset, Length: -1, ColonOffset: -1, Parent: current, Children: []*Node{}})
		}
	}
	visit(text, visitor{
		onObjectBegin: container(NodeObject),
		onObjectProperty: func(name string, offset, length int) {
			current = onValue(&Node{Type: NodeProperty, Offset: offset, Length: -1, ColonOffset: -1, Parent: current, Children: []*Node{}})
			current.Children = append(current.Children, &Node{Type: NodeString, Value: name, Offset: offset, Length: length, ColonOffset: -1, Parent: current})
		},
		onObjectEnd: func(offset, length int) {
			ensurePropertyComplete(offset + length) // in case of a missing value for a property
			current.Length = offset + length - current.Offset
			current = current.Parent
			ensurePropertyComplete(offset + length)
		},
		onArrayBegin: container(NodeArray),
		onArrayEnd: func(offset, length int) {
			current.Length = offset + length - current.Offset
			current = current.Parent
			ensurePropertyComplete(offset + length)
		},
		onLiteralValue: func(value any, offset, length int) {
			onValue(&Node{Type: nodeType(value), Value: value, Offset: offset, Length: length, ColonOffset: -1, Parent: current})
			ensurePropertyComplete(offset + length)
		},
		onSeparator: func(sep byte, offset, _ int) {
			if current.Type == NodeProperty {
				if sep == ':' {
					current.ColonOffset = offset
				} else if sep == ',' {
					ensurePropertyComplete(offset)
				}
			}
		},
		onError: func(code ParseErrorCode, offset, length int) {
			errs = append(errs, ParseError{code, offset, length})
		},
	}, options)
	if len(current.Children) == 0 {
		return nil, errs
	}
	result := current.Children[0]
	result.Parent = nil
	return result, errs
}

func nodeType(value any) NodeType {
	switch value.(type) {
	case bool:
		return NodeBoolean
	case float64:
		return NodeNumber
	case string:
		return NodeString
	}
	return NodeNull
}

// FindNodeAtLocation returns the node at path (string keys and int indices)
// or nil. For duplicate keys the first property wins.
func FindNodeAtLocation(root *Node, path []any) *Node {
	if root == nil {
		return nil
	}
	node := root
	for _, segment := range path {
		switch seg := segment.(type) {
		case string:
			if node.Type != NodeObject || node.Children == nil {
				return nil
			}
			found := false
			for _, prop := range node.Children {
				if prop.Children != nil && len(prop.Children) == 2 {
					if key, ok := prop.Children[0].Value.(string); ok && key == seg {
						node = prop.Children[1]
						found = true
						break
					}
				}
			}
			if !found {
				return nil
			}
		case int:
			if node.Type != NodeArray || seg < 0 || node.Children == nil || seg >= len(node.Children) {
				return nil
			}
			node = node.Children[seg]
		default:
			return nil
		}
	}
	return node
}
