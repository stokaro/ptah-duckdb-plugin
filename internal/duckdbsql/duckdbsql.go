// Package duckdbsql reads the statements DuckDB's catalog keeps for an index
// and a view. The catalog stores each object's CREATE statement in its own
// normalized spelling, and some facts are only there: the key parts of an
// index, in order, and the query of a view. The package tokenizes that
// spelling (identifiers, double-quoted names, single-quoted strings,
// comments and punctuation) and refuses a statement of any other shape, so a
// clause it does not know is never dropped.
package duckdbsql

import (
	"errors"
	"fmt"
	"strings"
)

// ErrShape is the error a statement of a shape the package does not read
// wraps.
var ErrShape = errors.New("unexpected DuckDB statement")

func shape(what string) error { return fmt.Errorf("%w: %s", ErrShape, what) }

// IndexPart is one part of an index key: a column, or an expression as the
// catalog writes it.
type IndexPart struct {
	Column     string
	Expression string
}

// Index is what CREATE INDEX states beyond the catalog's own columns.
type Index struct {
	Unique bool
	Parts  []IndexPart
	// Method is the USING clause, which the catalog writes only for an index
	// that is not DuckDB's own ART index.
	Method string
}

// ParseIndex reads `CREATE [UNIQUE] INDEX <name> ON <table> [USING <method>]
// (<part>, ...)` with an optional final semicolon. A part that is one
// identifier is a column; any other part is an expression.
func ParseIndex(statement string) (Index, error) {
	tokens, err := tokenize(statement)
	if err != nil {
		return Index{}, err
	}
	s := &scanner{tokens: tokens, text: statement}
	if !s.keyword("CREATE") {
		return Index{}, shape("an index statement that does not start with CREATE")
	}
	result := Index{Unique: s.keyword("UNIQUE")}
	if !s.keyword("INDEX") || !s.name() || !s.keyword("ON") || !s.qualifiedName() {
		return Index{}, shape("an index statement without its name and table")
	}
	if s.keyword("USING") {
		method, found := s.identifier()
		if !found {
			return Index{}, shape("an index method without a name")
		}
		result.Method = method
	}
	parts, err := s.parenthesized()
	if err != nil {
		return Index{}, err
	}
	for _, part := range parts {
		result.Parts = append(result.Parts, indexPart(part))
	}
	s.punct(";")
	if !s.done() {
		return Index{}, shape("an index statement with a clause after its key")
	}
	return result, nil
}

// indexPart reads one part of an index key.
func indexPart(part span) IndexPart {
	if len(part.tokens) == 1 && (part.tokens[0].kind == bareWord || part.tokens[0].kind == quotedName) {
		return IndexPart{Column: part.tokens[0].value}
	}
	return IndexPart{Expression: part.text}
}

// ErrViewColumns is the error ParseView returns for a view that names its
// columns, CREATE VIEW v (a, b) AS ...: the query alone does not say them.
var ErrViewColumns = errors.New("the view names its columns")

// ParseView reads `CREATE VIEW <name> AS <query>` with an optional final
// semicolon and returns the query as the catalog stores it.
func ParseView(statement string) (string, error) {
	tokens, err := tokenize(statement)
	if err != nil {
		return "", err
	}
	s := &scanner{tokens: tokens, text: statement}
	if !s.keyword("CREATE") || !s.keyword("VIEW") || !s.qualifiedName() {
		return "", shape("a view statement without its name")
	}
	if next, found := s.peek(); found && next.kind == punctuation && next.value == "(" {
		return "", ErrViewColumns
	}
	if !s.keyword("AS") || s.done() {
		return "", shape("a view statement without its query")
	}
	start := s.tokens[s.at].start
	end := len(statement)
	if last := s.tokens[len(s.tokens)-1]; last.kind == punctuation && last.value == ";" {
		end = last.start
	}
	query := strings.TrimSpace(statement[start:end])
	if query == "" {
		return "", shape("a view statement without its query")
	}
	return query, nil
}

// tokenKind is the class of a token.
type tokenKind int

const (
	bareWord tokenKind = iota + 1
	quotedName
	quotedString
	punctuation
	other
)

// token is one token and where it starts in the statement.
type token struct {
	kind  tokenKind
	value string
	start int
	end   int
}

// tokenize splits a statement into tokens. White space and comments separate
// tokens and are dropped; every other character is part of a token.
func tokenize(text string) ([]token, error) {
	var tokens []token
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case strings.HasPrefix(text[i:], "--"):
			for i < len(text) && text[i] != '\n' {
				i++
			}
		case strings.HasPrefix(text[i:], "/*"):
			end := strings.Index(text[i+2:], "*/")
			if end < 0 {
				return nil, shape("an unterminated comment")
			}
			i += end + 4
		case c == '"' || c == '\'':
			value, next, err := quoted(text, i)
			if err != nil {
				return nil, err
			}
			kind := quotedName
			if c == '\'' {
				kind = quotedString
			}
			tokens = append(tokens, token{kind: kind, value: value, start: i, end: next})
			i = next
		case wordByte(c):
			start := i
			for i < len(text) && wordByte(text[i]) {
				i++
			}
			tokens = append(tokens, token{kind: bareWord, value: text[start:i], start: start, end: i})
		case strings.IndexByte("(),.;", c) >= 0:
			tokens = append(tokens, token{kind: punctuation, value: string(c), start: i, end: i + 1})
			i++
		default:
			tokens = append(tokens, token{kind: other, value: string(c), start: i, end: i + 1})
			i++
		}
	}
	return tokens, nil
}

// wordByte reports whether c continues an unquoted identifier or a number.
func wordByte(c byte) bool {
	return c == '_' || c == '$' || (c >= '0' && c <= '9') || (c|0x20 >= 'a' && c|0x20 <= 'z') || c >= 0x80
}

// quoted reads the quoted text that starts at text[start], where a doubled
// quote stands for one, and returns its value and the index after it.
func quoted(text string, start int) (string, int, error) {
	quote := text[start]
	var value strings.Builder
	for i := start + 1; i < len(text); i++ {
		if text[i] != quote {
			value.WriteByte(text[i])
			continue
		}
		if i+1 < len(text) && text[i+1] == quote {
			value.WriteByte(quote)
			i++
			continue
		}
		return value.String(), i + 1, nil
	}
	return "", 0, shape("an unterminated quote")
}

// span is a run of tokens and the text it covers.
type span struct {
	tokens []token
	text   string
}

type scanner struct {
	tokens []token
	text   string
	at     int
}

func (s *scanner) done() bool { return s.at == len(s.tokens) }

func (s *scanner) peek() (token, bool) {
	if s.done() {
		return token{}, false
	}
	return s.tokens[s.at], true
}

// keyword accepts the bare word w, in any case.
func (s *scanner) keyword(w string) bool {
	next, found := s.peek()
	if !found || next.kind != bareWord || !strings.EqualFold(next.value, w) {
		return false
	}
	s.at++
	return true
}

func (s *scanner) punct(p string) bool {
	next, found := s.peek()
	if !found || next.kind != punctuation || next.value != p {
		return false
	}
	s.at++
	return true
}

// identifier accepts a bare word or a double-quoted name and returns its
// value.
func (s *scanner) identifier() (string, bool) {
	next, found := s.peek()
	if !found || (next.kind != bareWord && next.kind != quotedName) {
		return "", false
	}
	s.at++
	return next.value, true
}

func (s *scanner) name() bool {
	_, found := s.identifier()
	return found
}

// qualifiedName accepts a name with optional qualifiers: a.b.c.
func (s *scanner) qualifiedName() bool {
	if !s.name() {
		return false
	}
	for s.punct(".") {
		if !s.name() {
			return false
		}
	}
	return true
}

// parenthesized accepts a parenthesized list and returns its elements, split
// at the commas outside nested parentheses.
func (s *scanner) parenthesized() ([]span, error) {
	if !s.punct("(") {
		return nil, shape("a list without its parentheses")
	}
	var parts []span
	depth, first := 0, s.at
	for ; !s.done(); s.at++ {
		current := s.tokens[s.at]
		if current.kind != punctuation {
			continue
		}
		switch {
		case current.value == "(":
			depth++
		case current.value == ")" && depth > 0:
			depth--
		case (current.value == "," && depth == 0) || current.value == ")":
			if first == s.at {
				return nil, shape("an empty list element")
			}
			parts = append(parts, s.spanOf(first, s.at))
			first = s.at + 1
			if current.value == ")" {
				s.at++
				return parts, nil
			}
		}
	}
	return nil, shape("an unterminated list")
}

// spanOf is the tokens from index from up to index to, and their text.
func (s *scanner) spanOf(from, to int) span {
	tokens := s.tokens[from:to]
	return span{tokens: tokens, text: strings.TrimSpace(s.text[tokens[0].start:tokens[len(tokens)-1].end])}
}

// ColumnClauses are the clauses of one column's definition that DuckDB's
// catalog functions do not report, read from the table's CREATE statement.
type ColumnClauses struct {
	// Collation is the name after COLLATE, as written, or empty.
	Collation string
	// Compression is set by USING COMPRESSION.
	Compression bool
}

// constraintWords start a table constraint rather than a column.
var constraintWords = []string{"PRIMARY", "UNIQUE", "CHECK", "FOREIGN", "CONSTRAINT"}

// ParseTableColumns reads `CREATE TABLE <name> (<element>, ...)` with an
// optional final semicolon and returns the clauses of each column, by the
// column's name as written. A table constraint is skipped.
func ParseTableColumns(statement string) (map[string]ColumnClauses, error) {
	tokens, err := tokenize(statement)
	if err != nil {
		return nil, err
	}
	s := &scanner{tokens: tokens, text: statement}
	if !s.keyword("CREATE") || !s.keyword("TABLE") || !s.qualifiedName() {
		return nil, shape("a table statement without its name")
	}
	elements, err := s.parenthesized()
	if err != nil {
		return nil, err
	}
	s.punct(";")
	if !s.done() {
		return nil, shape("a table statement with a clause after its columns")
	}
	result := make(map[string]ColumnClauses, len(elements))
	for _, element := range elements {
		first := element.tokens[0]
		if first.kind == bareWord && slicesContainsFold(constraintWords, first.value) {
			continue
		}
		if first.kind != bareWord && first.kind != quotedName {
			return nil, shape("a column without a name")
		}
		result[first.value] = columnClauses(element.tokens[1:])
	}
	return result, nil
}

// columnClauses reads COLLATE and USING COMPRESSION outside parentheses.
func columnClauses(tokens []token) ColumnClauses {
	var clauses ColumnClauses
	depth := 0
	for i, current := range tokens {
		switch {
		case current.kind == punctuation && current.value == "(":
			depth++
		case current.kind == punctuation && current.value == ")":
			depth--
		case depth != 0 || current.kind != bareWord:
		case strings.EqualFold(current.value, "COLLATE"):
			clauses.Collation = collationName(tokens[i+1:])
		case strings.EqualFold(current.value, "USING") && i+1 < len(tokens) && strings.EqualFold(tokens[i+1].value, "COMPRESSION"):
			clauses.Compression = true
		}
	}
	return clauses
}

// collationName reads a collation name, which may be dotted: nocase.noaccent.
func collationName(tokens []token) string {
	var parts []string
	for i := 0; i < len(tokens); i += 2 {
		if tokens[i].kind != bareWord && tokens[i].kind != quotedName {
			break
		}
		parts = append(parts, tokens[i].value)
		if i+1 >= len(tokens) || tokens[i+1].kind != punctuation || tokens[i+1].value != "." {
			break
		}
	}
	return strings.Join(parts, ".")
}

func slicesContainsFold(words []string, word string) bool {
	for _, candidate := range words {
		if strings.EqualFold(candidate, word) {
			return true
		}
	}
	return false
}
