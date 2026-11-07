// Package sqltext splits a text of DuckDB's SQL into statements and says what
// each statement is, for the host's assessment of the statements a plan or a
// migration runs.
//
// The lexing follows DuckDB's: double-quoted identifiers and single-quoted
// strings with doubled quotes, E'...' strings with backslash escapes, dollar-
// quoted strings ($$...$$ and $tag$...$tag$), `--` and `/* */` comments, and a
// semicolon that ends a statement outside a quote or a comment.
package sqltext

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// Kind is what a statement does.
type Kind int

const (
	// Schema is a schema statement the host assesses as common DDL: CREATE,
	// DROP and ALTER of a table, an index, a view, a sequence or a schema,
	// and COMMENT ON.
	Schema Kind = iota + 1
	// Data changes or reads rows: INSERT, UPDATE, DELETE, SELECT and WITH.
	Data
	// Other is any other statement, such as SET, PRAGMA, CREATE MACRO or
	// COPY, which the provider does not assess.
	Other
)

// Statement is one statement of a text.
type Statement struct {
	// Start and End delimit the statement in the text, from its first token or
	// the comment before it to its semicolon, or to the end of the text.
	Start, End int
	// Line is the line Start is on, counted from 1.
	Line int
	// SourceWords are the tokens as written, and Words the same tokens with
	// every unquoted word in upper case.
	SourceWords, Words []string
	Kind               Kind
}

// Split splits a text into statements. Every byte of the text belongs to a
// statement or is white space between statements: a comment before a
// statement starts it, and a comment after the last one ends it.
func Split(text string) ([]Statement, error) {
	if uint64(len(text)) > math.MaxUint32 {
		return nil, fmt.Errorf("a SQL text is larger than the protocol can address")
	}
	var statements []Statement
	start := -1
	var source []string
	finish := func(end int) {
		statements = append(statements, classify(text, start, end, source))
		start, source = -1, nil
	}
	for position := 0; position < len(text); {
		if isSpace(text[position]) {
			position++
			continue
		}
		if start < 0 {
			start = position
		}
		end, err := tokenEnd(text, position)
		if err != nil {
			return nil, err
		}
		token := text[position:end]
		position = end
		switch {
		case strings.HasPrefix(token, "--") || strings.HasPrefix(token, "/*"):
			continue
		case token == ";":
			if len(source) == 0 {
				return nil, fmt.Errorf("an empty statement ends on line %d", 1+strings.Count(text[:end], "\n"))
			}
			finish(end)
		default:
			source = append(source, token)
		}
	}
	switch {
	case start >= 0 && len(source) > 0:
		finish(len(text))
	case start >= 0 && len(statements) == 0:
		return nil, fmt.Errorf("the SQL text holds comments and no statement")
	case start >= 0:
		// Comments after the last statement end it.
		statements[len(statements)-1].End = len(text)
	}
	return statements, nil
}

// operators are the characters an operator token is made of.
const operators = "+-*/<>=!~%^&|?:#@"

// tokenEnd returns where the token that starts at position ends.
func tokenEnd(text string, position int) (int, error) {
	character := text[position]
	switch {
	case strings.HasPrefix(text[position:], "--") || strings.HasPrefix(text[position:], "/*"):
		return commentEnd(text, position)
	case (character == 'e' || character == 'E') && position+1 < len(text) && text[position+1] == '\'':
		return escapedEnd(text, position+1)
	case character == '"' || character == '\'':
		return quotedEnd(text, position, character)
	case character == '$':
		return dollarEnd(text, position)
	case strings.IndexByte("(),;[].{}", character) >= 0:
		return position + 1, nil
	case isWordStart(character):
		return runEnd(text, position, isWordPart), nil
	case character >= '0' && character <= '9':
		return runEnd(text, position, isNumberPart), nil
	case strings.IndexByte(operators, character) >= 0:
		return operatorEnd(text, position), nil
	default:
		return 0, fmt.Errorf("the SQL text holds a character the provider does not read on line %d", 1+strings.Count(text[:position], "\n"))
	}
}

// commentEnd returns the end of the comment at position: a line comment ends
// before its newline, a block comment after its closing mark.
func commentEnd(text string, position int) (int, error) {
	if strings.HasPrefix(text[position:], "--") {
		if newline := strings.IndexByte(text[position:], '\n'); newline >= 0 {
			return position + newline, nil
		}
		return len(text), nil
	}
	end := strings.Index(text[position+2:], "*/")
	if end < 0 {
		return 0, fmt.Errorf("a comment is not closed")
	}
	return position + 2 + end + 2, nil
}

// dollarEnd returns the end of a dollar-quoted string, or of a parameter
// such as $1.
func dollarEnd(text string, position int) (int, error) {
	tagEnd := position + 1
	for tagEnd < len(text) && (isWordPart(text[tagEnd]) && text[tagEnd] != '$') {
		tagEnd++
	}
	if tagEnd >= len(text) || text[tagEnd] != '$' {
		if tagEnd > position+1 && text[position+1] >= '0' && text[position+1] <= '9' {
			return tagEnd, nil
		}
		return 0, fmt.Errorf("a dollar sign that starts no quote or parameter on line %d", 1+strings.Count(text[:position], "\n"))
	}
	tag := text[position : tagEnd+1]
	closing := strings.Index(text[tagEnd+1:], tag)
	if closing < 0 {
		return 0, fmt.Errorf("a dollar-quoted string is not closed")
	}
	return tagEnd + 1 + closing + len(tag), nil
}

// escapedEnd returns the end of an E'...' string, where a backslash escapes the
// next character and a doubled quote stands for one.
func escapedEnd(text string, quote int) (int, error) {
	for end := quote + 1; end < len(text); end++ {
		switch {
		case text[end] == '\\':
			end++
		case text[end] == '\'' && end+1 < len(text) && text[end+1] == '\'':
			end++
		case text[end] == '\'':
			return end + 1, nil
		}
	}
	return 0, fmt.Errorf("a string is not closed")
}

// runEnd returns the end of the run of characters part accepts that starts
// at position.
func runEnd(text string, position int, part func(byte) bool) int {
	end := position + 1
	for end < len(text) && part(text[end]) {
		end++
	}
	return end
}

// operatorEnd returns the end of an operator, which stops before a comment.
func operatorEnd(text string, position int) int {
	end := position + 1
	for end < len(text) && strings.IndexByte(operators, text[end]) >= 0 && !strings.HasPrefix(text[end:], "--") && !strings.HasPrefix(text[end:], "/*") {
		end++
	}
	return end
}

func isNumberPart(character byte) bool {
	return character >= '0' && character <= '9' || character == '.' || character == 'e' || character == 'E' || character == '_'
}

// quotedEnd returns the end of a quoted identifier or string, where a doubled
// quote stands for one.
func quotedEnd(text string, position int, quote byte) (int, error) {
	for end := position + 1; end < len(text); end++ {
		if text[end] != quote {
			continue
		}
		if end+1 < len(text) && text[end+1] == quote {
			end++
			continue
		}
		return end + 1, nil
	}
	if quote == '"' {
		return 0, fmt.Errorf("a quoted name is not closed")
	}
	return 0, fmt.Errorf("a string is not closed")
}

// classify describes the statement spanning text[start:end].
func classify(text string, start, end int, source []string) Statement {
	words := make([]string, len(source))
	for i, word := range source {
		words[i] = word
		if isWord(word) {
			words[i] = strings.ToUpper(word)
		}
	}
	return Statement{Start: start, End: end, Line: 1 + strings.Count(text[:start], "\n"), SourceWords: source, Words: words, Kind: kindOf(words)}
}

// schemaObjects are the objects whose CREATE, DROP and ALTER are schema
// statements the host assesses.
var schemaObjects = []string{"TABLE", "INDEX", "VIEW", "SEQUENCE", "SCHEMA"}

// kindOf reads the statement's leading words.
func kindOf(words []string) Kind {
	switch words[0] {
	case "INSERT", "UPDATE", "DELETE", "SELECT", "WITH":
		return Data
	case "COMMENT":
		if len(words) > 1 && words[1] == "ON" {
			return Schema
		}
	case "CREATE", "DROP", "ALTER":
		rest := words[1:]
		if len(rest) >= 2 && rest[0] == "OR" && rest[1] == "REPLACE" {
			rest = rest[2:]
		}
		if len(rest) >= 1 && rest[0] == "UNIQUE" {
			rest = rest[1:]
		}
		if len(rest) >= 1 && slices.Contains(schemaObjects, rest[0]) {
			return Schema
		}
	}
	return Other
}

func isSpace(character byte) bool { return strings.IndexByte(" \t\r\n\f\v", character) >= 0 }

func isWordStart(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character == '_' || character >= 0x80
}

// isWord reports whether a token is an unquoted word, which DuckDB reads in
// any case; a string such as E'x' starts with a letter and is not one.
func isWord(token string) bool {
	if !isWordStart(token[0]) {
		return false
	}
	for i := 1; i < len(token); i++ {
		if !isWordPart(token[i]) {
			return false
		}
	}
	return true
}

func isWordPart(character byte) bool {
	return isWordStart(character) || character >= '0' && character <= '9' || character == '$'
}
