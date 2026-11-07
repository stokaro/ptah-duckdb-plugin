// Package sqltext splits a text of DuckDB's SQL into statements and says what
// each statement is, for the host's assessment of the statements a plan or a
// migration runs.
//
// The lexing is DuckDB's, which is PostgreSQL's but for a segment that
// continues an escape string, and comes from
// github.com/stokaro/ptah/sqlsyntax/pgsyntax, the module the host reads the
// PostgreSQL family with: quoted names, string constants, dollar-quoted
// strings, nested block comments, and a semicolon that ends a statement
// outside all of them.
package sqltext

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/stokaro/ptah/sqlsyntax/pgsyntax"
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

// options are the lexical rules DuckDB reads text with, measured with
// duckdb-go v2.20000.0 (DuckDB v2.0.0): PostgreSQL's with
// standard_conforming_strings on, and a segment that continues an escape
// string scanned as an ordinary string.
var options = pgsyntax.Options{PlainContinuations: true}

// Split splits a text into statements. Every byte of the text belongs to a
// statement or is white space between statements: a comment before a
// statement starts it, and a comment after the last one ends it. A string, a
// quoted name, a dollar-quoted string or a block comment that does not close
// fails the whole text, naming the line it opens on.
func Split(text string) ([]Statement, error) {
	if uint64(len(text)) > math.MaxUint32 {
		return nil, fmt.Errorf("a SQL text is larger than the protocol can address")
	}
	tokens, err := pgsyntax.Scan(text, options)
	if err != nil {
		return nil, lexError(text, err)
	}
	var statements []Statement
	start := -1
	var source []pgsyntax.Token
	finish := func(end int) {
		statements = append(statements, classify(text, start, end, source))
		start, source = -1, nil
	}
	for _, token := range tokens {
		if token.Kind == pgsyntax.Whitespace {
			continue
		}
		if start < 0 {
			start = token.Start
		}
		switch {
		case token.Kind == pgsyntax.LineComment || token.Kind == pgsyntax.BlockComment:
			continue
		case token.Kind == pgsyntax.Punctuation && text[token.Start] == ';':
			if len(source) == 0 {
				return nil, fmt.Errorf("an empty statement ends on line %d", 1+strings.Count(text[:token.End], "\n"))
			}
			finish(token.End)
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

// lexError places a scanner's refusal on the line its construct opens on.
func lexError(text string, err error) error {
	syntax, ok := errors.AsType[*pgsyntax.SyntaxError](err)
	if !ok {
		return err
	}
	return fmt.Errorf("%s on line %d", syntax.Reason, 1+strings.Count(text[:syntax.Offset], "\n"))
}

// classify describes the statement spanning text[start:end], whose tokens
// are source with white space and comments left out. An unquoted word, which
// DuckDB reads in any case, is upper-cased in Words; a string such as E'x' is
// not a word.
func classify(text string, start, end int, source []pgsyntax.Token) Statement {
	written := make([]string, len(source))
	words := make([]string, len(source))
	for i, token := range source {
		written[i] = token.Text(text)
		words[i] = written[i]
		if token.Kind == pgsyntax.Word {
			words[i] = strings.ToUpper(written[i])
		}
	}
	return Statement{Start: start, End: end, Line: 1 + strings.Count(text[:start], "\n"), SourceWords: written, Words: words, Kind: kindOf(words)}
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
