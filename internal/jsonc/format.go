package jsonc

import (
	"errors"
	"strings"
)

// Port of lib/esm/impl/format.js.

// Edit is a text edit: replace Length bytes at Offset with Content.
type Edit struct {
	Offset  int
	Length  int
	Content string
}

// Range is a byte range of a document.
type Range struct {
	Offset int
	Length int
}

// FormattingOptions mirrors upstream's FormattingOptions. TabSize 0 means
// upstream's default of 4. EOL is used only when the document has no line
// break; empty means "\n".
type FormattingOptions struct {
	TabSize            int
	InsertSpaces       bool
	EOL                string
	InsertFinalNewline bool
	KeepLines          bool
}

// errIndentCache reproduces an upstream defect: an indent of exactly 200
// characters indexes one past the end of a 200-entry cache, so upstream
// produces an edit with undefined content (and setProperty then throws a
// TypeError). Format and Modify return this error instead when such an
// edit would be produced.
var errIndentCache = errors.New("jsonc: indentation of exactly 200 characters is not supported (upstream jsonc-parser produces undefined content)")

const maxCachedValues = 200

// undefinedContent stands in for upstream's undefined cache lookup until an
// edit actually uses it.
const undefinedContent = "\x00jsonc:undefined\x00"

// Format computes the edits that format text, or only the lines touched by
// rng when it is non-nil.
func Format(documentText string, rng *Range, options FormattingOptions) ([]Edit, error) {
	var initialIndentLevel, formatTextStart, rangeStart, rangeEnd int
	var formatText string
	if rng != nil {
		rangeStart = rng.Offset
		rangeEnd = rangeStart + rng.Length
		formatTextStart = rangeStart
		for formatTextStart > 0 && !isEOL(documentText, formatTextStart-1) {
			formatTextStart--
		}
		endOffset := rangeEnd
		for endOffset < len(documentText) && !isEOL(documentText, endOffset) {
			endOffset++
		}
		formatText = substr(documentText, formatTextStart, endOffset)
		initialIndentLevel = computeIndentLevel(formatText, options)
	} else {
		formatText = documentText
		rangeEnd = len(documentText)
	}
	eol := getEOL(options, documentText)
	eolFastPathSupported := eol == "\n" || eol == "\r" || eol == "\r\n"
	numberLineBreaks := 0
	indentLevel := 0
	tabSize := options.TabSize
	if tabSize == 0 {
		tabSize = 4
	}
	indentValue := "\t"
	if options.InsertSpaces {
		indentValue = repeat(" ", tabSize)
	}
	sc := newScanner(formatText)
	hasError := false
	var cacheErr error

	newLinesAndIndent := func() string {
		if numberLineBreaks > 1 {
			return repeat(eol, numberLineBreaks) + repeat(indentValue, initialIndentLevel+indentLevel)
		}
		amountOfSpaces := len(indentValue) * (initialIndentLevel + indentLevel)
		if !eolFastPathSupported || amountOfSpaces > maxCachedValues {
			return eol + repeat(indentValue, initialIndentLevel+indentLevel)
		}
		if amountOfSpaces <= 0 {
			return eol
		}
		if amountOfSpaces == maxCachedValues {
			return undefinedContent
		}
		// The cache holds eol followed by amountOfSpaces copies of the
		// indent character (a space or a tab).
		return eol + repeat(indentValue[:1], amountOfSpaces)
	}
	scanNext := func() syntaxKind {
		token := sc.scan()
		numberLineBreaks = 0
		for token == tokTrivia || token == tokLineBreak {
			if token == tokLineBreak && options.KeepLines {
				numberLineBreaks++
			} else if token == tokLineBreak {
				numberLineBreaks = 1
			}
			token = sc.scan()
		}
		hasError = token == tokUnknown || sc.err != scanErrNone
		return token
	}
	var edits []Edit
	addEdit := func(text string, startOffset, endOffset int) {
		if !hasError && (rng == nil || (startOffset < rangeEnd && endOffset > rangeStart)) && substr(documentText, startOffset, endOffset) != text {
			if text == undefinedContent {
				cacheErr = errIndentCache
			}
			edits = append(edits, Edit{Offset: startOffset, Length: endOffset - startOffset, Content: text})
		}
	}

	firstToken := scanNext()
	if options.KeepLines && numberLineBreaks > 0 {
		addEdit(repeat(eol, numberLineBreaks), 0, 0)
	}
	if firstToken != tokEOF {
		firstTokenStart := sc.tokenOffset + formatTextStart
		addEdit(repeat(indentValue, initialIndentLevel), formatTextStart, firstTokenStart)
	}
	for firstToken != tokEOF {
		firstTokenEnd := sc.tokenOffset + sc.tokenLength() + formatTextStart
		secondToken := scanNext()
		replaceContent := ""
		needsLineBreak := false
		for numberLineBreaks == 0 && (secondToken == tokLineComment || secondToken == tokBlockComment) {
			commentTokenStart := sc.tokenOffset + formatTextStart
			addEdit(" ", firstTokenEnd, commentTokenStart)
			firstTokenEnd = sc.tokenOffset + sc.tokenLength() + formatTextStart
			needsLineBreak = secondToken == tokLineComment
			if needsLineBreak {
				replaceContent = newLinesAndIndent()
			} else {
				replaceContent = ""
			}
			secondToken = scanNext()
		}
		if secondToken == tokCloseBrace {
			if firstToken != tokOpenBrace {
				indentLevel--
			}
			if options.KeepLines && numberLineBreaks > 0 || !options.KeepLines && firstToken != tokOpenBrace {
				replaceContent = newLinesAndIndent()
			} else if options.KeepLines {
				replaceContent = " "
			}
		} else if secondToken == tokCloseBracket {
			if firstToken != tokOpenBracket {
				indentLevel--
			}
			if options.KeepLines && numberLineBreaks > 0 || !options.KeepLines && firstToken != tokOpenBracket {
				replaceContent = newLinesAndIndent()
			} else if options.KeepLines {
				replaceContent = " "
			}
		} else {
			switch firstToken {
			case tokOpenBracket, tokOpenBrace:
				indentLevel++
				if options.KeepLines && numberLineBreaks > 0 || !options.KeepLines {
					replaceContent = newLinesAndIndent()
				} else {
					replaceContent = " "
				}
			case tokComma:
				if options.KeepLines && numberLineBreaks > 0 || !options.KeepLines {
					replaceContent = newLinesAndIndent()
				} else {
					replaceContent = " "
				}
			case tokLineComment:
				replaceContent = newLinesAndIndent()
			case tokBlockComment:
				if numberLineBreaks > 0 {
					replaceContent = newLinesAndIndent()
				} else if !needsLineBreak {
					replaceContent = " "
				}
			case tokColon:
				if options.KeepLines && numberLineBreaks > 0 {
					replaceContent = newLinesAndIndent()
				} else if !needsLineBreak {
					replaceContent = " "
				}
			case tokString:
				if options.KeepLines && numberLineBreaks > 0 {
					replaceContent = newLinesAndIndent()
				} else if secondToken == tokColon && !needsLineBreak {
					replaceContent = ""
				}
			case tokNull, tokTrue, tokFalse, tokNumber, tokCloseBrace, tokCloseBracket:
				if options.KeepLines && numberLineBreaks > 0 {
					replaceContent = newLinesAndIndent()
				} else if (secondToken == tokLineComment || secondToken == tokBlockComment) && !needsLineBreak {
					replaceContent = " "
				} else if secondToken != tokComma && secondToken != tokEOF {
					hasError = true
				}
			case tokUnknown:
				hasError = true
			}
			if numberLineBreaks > 0 && (secondToken == tokLineComment || secondToken == tokBlockComment) {
				replaceContent = newLinesAndIndent()
			}
		}
		if secondToken == tokEOF {
			if options.KeepLines && numberLineBreaks > 0 {
				replaceContent = newLinesAndIndent()
			} else if options.InsertFinalNewline {
				replaceContent = eol
			} else {
				replaceContent = ""
			}
		}
		secondTokenStart := sc.tokenOffset + formatTextStart
		addEdit(replaceContent, firstTokenEnd, secondTokenStart)
		firstToken = secondToken
	}
	if cacheErr != nil {
		return nil, cacheErr
	}
	return edits, nil
}

// repeat mirrors upstream's repeat helper: a non-positive count yields "".
func repeat(s string, count int) string {
	if count <= 0 {
		return ""
	}
	return strings.Repeat(s, count)
}

func computeIndentLevel(content string, options FormattingOptions) int {
	nChars := 0
	tabSize := options.TabSize
	if tabSize == 0 {
		tabSize = 4
	}
	for i := 0; i < len(content); i++ {
		switch content[i] {
		case ' ':
			nChars++
		case '\t':
			nChars += tabSize
		default:
			return floorDiv(nChars, tabSize)
		}
	}
	return floorDiv(nChars, tabSize)
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func getEOL(options FormattingOptions, text string) string {
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\r':
			if i+1 < len(text) && text[i+1] == '\n' {
				return "\r\n"
			}
			return "\r"
		case '\n':
			return "\n"
		}
	}
	if options.EOL != "" {
		return options.EOL
	}
	return "\n"
}

// isEOL mirrors upstream, including that an out-of-range offset counts as
// an end of line (upstream's isEOL tests '\r\n'.indexOf(text.charAt(offset)), and charAt past the end is the empty string, found at index 0).
func isEOL(text string, offset int) bool {
	if offset < 0 || offset >= len(text) {
		return true
	}
	return text[offset] == '\r' || text[offset] == '\n'
}
