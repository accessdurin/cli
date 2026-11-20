package jsonc

import (
	"errors"
	"fmt"
	"sort"
)

// Port of lib/esm/impl/edit.js (setProperty, withFormatting, applyEdit) and
// main.js applyEdits.

// ModificationOptions mirrors upstream's ModificationOptions. When
// FormattingOptions is nil the raw edit is returned unformatted. Upstream's
// isArrayInsertion and getInsertionIndex options are not ported.
type ModificationOptions struct {
	FormattingOptions *FormattingOptions
}

// Modify computes the edits that set the value at path, creating missing
// parents (objects for string segments, arrays for int segments). Path
// segments are string keys or int indices; an index equal to (or greater
// than) the array length appends, and -1 also appends. The inserted text is
// Stringify(value), formatted with the surrounding lines when
// opts.FormattingOptions is set. Apply the result with ApplyEdits.
//
// Edits are byte-offset based; applying them yields the same text as
// upstream's applyEdits(text, modify(text, path, value, options)).
func Modify(text string, path []any, value any, opts ModificationOptions) ([]Edit, error) {
	return setProperty(text, path, value, false, opts)
}

// Remove computes the edits that remove the value at path (upstream's
// modify with value undefined). Removing a missing object property yields no
// edits. Like upstream, index -1 takes the append branch and inserts the
// text undefined; other out-of-range indices are errors (TypeErrors
// upstream).
func Remove(text string, path []any, opts ModificationOptions) ([]Edit, error) {
	return setProperty(text, path, nil, true, opts)
}

func setProperty(text string, originalPath []any, value any, remove bool, opts ModificationOptions) ([]Edit, error) {
	for _, seg := range originalPath {
		switch seg.(type) {
		case string, int:
		default:
			return nil, fmt.Errorf("jsonc: path segment %v has type %T; want string or int", seg, seg)
		}
	}
	path := append([]any(nil), originalPath...)
	root, _ := ParseTree(text, ParseOptions{})
	var parent *Node
	var lastSegment any
	for len(path) > 0 {
		lastSegment = path[len(path)-1]
		path = path[:len(path)-1]
		parent = FindNodeAtLocation(root, path)
		if parent == nil && !remove {
			if key, ok := lastSegment.(string); ok {
				value = NewObject().Set(key, value)
			} else {
				value = []any{value}
			}
		} else {
			break
		}
	}

	stringifyValue := func() (string, error) {
		if remove {
			// JSON.stringify(undefined) interpolated into a template
			// literal (only reachable for index -1).
			return "undefined", nil
		}
		return Stringify(value)
	}

	if parent == nil {
		// empty document
		if remove {
			return nil, errors.New("Can not delete in empty document")
		}
		content, err := Stringify(value)
		if err != nil {
			return nil, err
		}
		e := Edit{Content: content}
		if root != nil {
			e.Offset, e.Length = root.Offset, root.Length
		}
		return withFormatting(text, e, opts)
	}

	key, isKey := lastSegment.(string)
	index, isIndex := lastSegment.(int)
	switch {
	case parent.Type == NodeObject && isKey && parent.Children != nil:
		existing := FindNodeAtLocation(parent, []any{key})
		if existing != nil {
			if remove {
				if existing.Parent == nil {
					return nil, errors.New("Malformed AST")
				}
				propertyIndex := indexOf(parent.Children, existing.Parent)
				var removeBegin int
				removeEnd := existing.Parent.Offset + existing.Parent.Length
				if propertyIndex > 0 {
					// remove the comma of the previous node
					previous := parent.Children[propertyIndex-1]
					removeBegin = previous.Offset + previous.Length
				} else {
					removeBegin = parent.Offset + 1
					if len(parent.Children) > 1 {
						// remove the comma of the next node
						removeEnd = parent.Children[1].Offset
					}
				}
				return withFormatting(text, Edit{Offset: removeBegin, Length: removeEnd - removeBegin}, opts)
			}
			// set value of existing property
			content, err := Stringify(value)
			if err != nil {
				return nil, err
			}
			return withFormatting(text, Edit{Offset: existing.Offset, Length: existing.Length, Content: content}, opts)
		}
		if remove {
			return []Edit{}, nil // property does not exist, nothing to do
		}
		valueText, err := Stringify(value)
		if err != nil {
			return nil, err
		}
		keyText, _ := Stringify(key)
		newProperty := keyText + ": " + valueText
		var e Edit
		if n := len(parent.Children); n > 0 {
			previous := parent.Children[n-1]
			e = Edit{Offset: previous.Offset + previous.Length, Content: "," + newProperty}
		} else {
			e = Edit{Offset: parent.Offset + 1, Content: newProperty}
		}
		return withFormatting(text, e, opts)

	case parent.Type == NodeArray && isIndex && parent.Children != nil:
		children := parent.Children
		if index == -1 {
			// Insert
			newProperty, err := stringifyValue()
			if err != nil {
				return nil, err
			}
			var e Edit
			if len(children) == 0 {
				e = Edit{Offset: parent.Offset + 1, Content: newProperty}
			} else {
				previous := children[len(children)-1]
				e = Edit{Offset: previous.Offset + previous.Length, Content: "," + newProperty}
			}
			return withFormatting(text, e, opts)
		}
		if remove {
			// Removal
			var e Edit
			switch {
			case len(children) == 1:
				// only item
				e = Edit{Offset: parent.Offset + 1, Length: parent.Length - 2}
			case len(children)-1 == index:
				// last item
				previous := children[index-1]
				offset := previous.Offset + previous.Length
				parentEndOffset := parent.Offset + parent.Length
				e = Edit{Offset: offset, Length: parentEndOffset - 2 - offset}
			case index >= 0 && index+1 < len(children):
				e = Edit{Offset: children[index].Offset, Length: children[index+1].Offset - children[index].Offset}
			default:
				return nil, fmt.Errorf("jsonc: cannot remove array index %d: array has %d elements", index, len(children))
			}
			return withFormatting(text, e, opts)
		}
		newProperty, err := Stringify(value)
		if err != nil {
			return nil, err
		}
		var e Edit
		switch {
		case len(children) > index:
			if index < 0 {
				return nil, fmt.Errorf("jsonc: cannot modify array index %d", index)
			}
			toModify := children[index]
			e = Edit{Offset: toModify.Offset, Length: toModify.Length, Content: newProperty}
		case len(children) == 0 || index == 0:
			e = Edit{Offset: parent.Offset + 1, Content: newProperty}
		default:
			i := index
			if i > len(children) {
				i = len(children)
			}
			previous := children[i-1]
			e = Edit{Offset: previous.Offset + previous.Length, Content: "," + newProperty}
		}
		return withFormatting(text, e, opts)
	}
	what := "property"
	if !isIndex {
		what = "index"
	}
	return nil, fmt.Errorf("Can not add %s to parent of type %s", what, parent.Type)
}

func indexOf(nodes []*Node, n *Node) int {
	for i, c := range nodes {
		if c == n {
			return i
		}
	}
	return -1
}

func withFormatting(text string, e Edit, opts ModificationOptions) ([]Edit, error) {
	if opts.FormattingOptions == nil {
		return []Edit{e}, nil
	}
	// apply the edit
	newText := applyEdit(text, e)
	// format the new text
	begin := e.Offset
	end := e.Offset + len(e.Content)
	if e.Length == 0 || len(e.Content) == 0 { // insert or remove
		for begin > 0 && !isEOL(newText, begin-1) {
			begin--
		}
		for end < len(newText) && !isEOL(newText, end) {
			end++
		}
	}
	fo := *opts.FormattingOptions
	fo.KeepLines = false
	edits, err := Format(newText, &Range{Offset: begin, Length: end - begin}, fo)
	if err != nil {
		return nil, err
	}
	// apply the formatting edits and track the begin and end offsets of the changes
	for i := len(edits) - 1; i >= 0; i-- {
		fe := edits[i]
		newText = applyEdit(newText, fe)
		begin = min(begin, fe.Offset)
		end = max(end, fe.Offset+fe.Length)
		end += len(fe.Content) - fe.Length
	}
	// create a single edit with all changes
	editLength := len(text) - (len(newText) - end) - begin
	return []Edit{{Offset: begin, Length: editLength, Content: substr(newText, begin, end)}}, nil
}

func applyEdit(text string, e Edit) string {
	return substr(text, 0, e.Offset) + e.Content + substr(text, e.Offset+e.Length, len(text))
}

// ApplyEdits applies edits (in any order) to text. Overlapping edits are an
// error, as upstream.
func ApplyEdits(text string, edits []Edit) (string, error) {
	sorted := append([]Edit(nil), edits...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Offset != sorted[j].Offset {
			return sorted[i].Offset < sorted[j].Offset
		}
		return sorted[i].Length < sorted[j].Length
	})
	lastModifiedOffset := len(text)
	for i := len(sorted) - 1; i >= 0; i-- {
		e := sorted[i]
		if e.Offset+e.Length > lastModifiedOffset {
			return "", errors.New("Overlapping edit")
		}
		text = applyEdit(text, e)
		lastModifiedOffset = e.Offset
	}
	return text, nil
}
