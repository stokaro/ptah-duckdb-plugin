// Package coltype reads a column type as DuckDB's catalog writes it into a
// structured type, and writes it back in the same spelling.
//
// The catalog spells every type in one canonical form: aliases are resolved
// (TEXT and STRING read as VARCHAR, INT8 as BIGINT, NUMERIC as DECIMAL), a
// named type created with CREATE TYPE reads as the type it stands for, and a
// nested type spells its parts in full: INTEGER[], VARCHAR[3][],
// STRUCT("a b" INTEGER, c VARCHAR[]), MAP(VARCHAR, INTEGER),
// UNION(i INTEGER, s VARCHAR) and ENUM('sad', 'ok'). Parse accepts exactly
// the types the embedded DuckDB has and refuses any other, so a type it does
// not know, such as one an extension adds, is never read as something else.
package coltype

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ErrUnknown is the error Parse returns for a type it does not read.
var ErrUnknown = errors.New("unknown DuckDB type")

// Kind is the shape of a type.
type Kind int

// The shapes of a type.
const (
	// Base is a type with no parameter, such as INTEGER or TIMESTAMP WITH
	// TIME ZONE.
	Base Kind = iota + 1
	// Decimal is DECIMAL(precision,scale).
	Decimal
	// Enum is ENUM with its labels in order.
	Enum
	// List is a list of any length of one element type: T[].
	List
	// Array is a list of a fixed length: T[N].
	Array
	// Struct is STRUCT with named fields in order.
	Struct
	// Map is MAP(key, value).
	Map
	// Union is UNION with named members in order.
	Union
)

// Type is one DuckDB column type.
type Type struct {
	Kind Kind
	// Name is the canonical name of a Base type.
	Name string
	// Precision and Scale are a Decimal's.
	Precision, Scale int
	// Labels are an Enum's, in order.
	Labels []string
	// Elem is the element type of a List or an Array, and Size an Array's
	// length.
	Elem *Type
	Size int
	// Fields are a Struct's fields or a Union's members, in order.
	Fields []Field
	// Key and Value are a Map's.
	Key, Value *Type
}

// Field is a named part of a Struct or a Union. Quoted records whether the
// catalog wrote the name in double quotes, so String writes it back the same
// way.
type Field struct {
	Name   string
	Quoted bool
	Type   Type
}

// baseNames are the types without parameters the embedded DuckDB has, in the
// spelling its catalog writes. Two of them are several words.
var baseNames = []string{
	"BOOLEAN",
	"TINYINT", "SMALLINT", "INTEGER", "BIGINT", "HUGEINT",
	"UTINYINT", "USMALLINT", "UINTEGER", "UBIGINT", "UHUGEINT",
	"FLOAT", "DOUBLE", "BIGNUM",
	"VARCHAR", "JSON", "BLOB", "BIT", "UUID",
	"DATE", "TIME", "TIME_NS", "TIME WITH TIME ZONE",
	"TIMESTAMP", "TIMESTAMP_S", "TIMESTAMP_MS", "TIMESTAMP_NS", "TIMESTAMP WITH TIME ZONE",
	"INTERVAL", "GEOMETRY", "VARIANT",
}

// maxDecimalPrecision is the widest DECIMAL DuckDB stores.
const maxDecimalPrecision = 38

// Parse reads a type as DuckDB's catalog writes it.
func Parse(text string) (Type, error) {
	tokens, err := tokenize(text)
	if err != nil {
		return Type{}, err
	}
	p := &parser{tokens: tokens}
	parsed, err := p.parseType()
	if err != nil {
		return Type{}, err
	}
	if !p.done() {
		return Type{}, unknown("text after the type")
	}
	return parsed, nil
}

func unknown(what string) error { return fmt.Errorf("%w: %s", ErrUnknown, what) }

// String writes the type in the spelling DuckDB's catalog uses.
func (t Type) String() string {
	var b strings.Builder
	t.write(&b)
	return b.String()
}

func (t Type) write(b *strings.Builder) {
	switch t.Kind {
	case Base:
		b.WriteString(t.Name)
	case Decimal:
		fmt.Fprintf(b, "DECIMAL(%d,%d)", t.Precision, t.Scale)
	case Enum:
		b.WriteString("ENUM(")
		for i, label := range t.Labels {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("'" + strings.ReplaceAll(label, "'", "''") + "'")
		}
		b.WriteString(")")
	case List:
		t.Elem.write(b)
		b.WriteString("[]")
	case Array:
		t.Elem.write(b)
		fmt.Fprintf(b, "[%d]", t.Size)
	case Struct, Union:
		b.WriteString(map[Kind]string{Struct: "STRUCT(", Union: "UNION("}[t.Kind])
		for i, field := range t.Fields {
			if i > 0 {
				b.WriteString(", ")
			}
			if field.Quoted {
				b.WriteString(`"` + strings.ReplaceAll(field.Name, `"`, `""`) + `"`)
			} else {
				b.WriteString(field.Name)
			}
			b.WriteString(" ")
			field.Type.write(b)
		}
		b.WriteString(")")
	case Map:
		b.WriteString("MAP(")
		t.Key.write(b)
		b.WriteString(", ")
		t.Value.write(b)
		b.WriteString(")")
	}
}

// tokenKind is the class of a token.
type tokenKind int

const (
	word tokenKind = iota + 1
	quotedName
	quotedLabel
	number
	punct
)

type token struct {
	kind tokenKind
	text string
}

// tokenize splits a type into words, double-quoted names, single-quoted
// labels, numbers and the punctuation ( ) [ ] and comma.
func tokenize(text string) ([]token, error) {
	var tokens []token
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == ' ':
			i++
		case strings.IndexByte("()[],", c) >= 0:
			tokens = append(tokens, token{punct, string(c)})
			i++
		case c == '"' || c == '\'':
			value, next, err := quoted(text, i)
			if err != nil {
				return nil, err
			}
			kind := quotedName
			if c == '\'' {
				kind = quotedLabel
			}
			tokens = append(tokens, token{kind, value})
			i = next
		case isDigit(c):
			start := i
			for i < len(text) && isDigit(text[i]) {
				i++
			}
			tokens = append(tokens, token{number, text[start:i]})
		case isWordStart(c):
			start := i
			for i < len(text) && (isWordStart(text[i]) || isDigit(text[i])) {
				i++
			}
			tokens = append(tokens, token{word, text[start:i]})
		default:
			return nil, unknown("an unexpected character")
		}
	}
	return tokens, nil
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isWordStart reports whether c starts an unquoted word: an ASCII letter or
// an underscore.
func isWordStart(c byte) bool { return c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z') }

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
	return "", 0, unknown("an unterminated quote")
}

type parser struct {
	tokens []token
	at     int
}

func (p *parser) done() bool { return p.at == len(p.tokens) }

func (p *parser) peek() (token, bool) {
	if p.done() {
		return token{}, false
	}
	return p.tokens[p.at], true
}

func (p *parser) accept(kind tokenKind, text string) bool {
	next, ok := p.peek()
	if !ok || next.kind != kind || (text != "" && next.text != text) {
		return false
	}
	p.at++
	return true
}

func (p *parser) expect(kind tokenKind, text string) (token, error) {
	next, ok := p.peek()
	if !ok || next.kind != kind || (text != "" && next.text != text) {
		return token{}, unknown("a malformed type")
	}
	p.at++
	return next, nil
}

// parseType reads a type and its list and array suffixes.
func (p *parser) parseType() (Type, error) {
	parsed, err := p.parsePrimary()
	if err != nil {
		return Type{}, err
	}
	for p.accept(punct, "[") {
		elem := parsed
		if p.accept(punct, "]") {
			parsed = Type{Kind: List, Elem: &elem}
			continue
		}
		size, err := p.number()
		if err != nil {
			return Type{}, err
		}
		if _, err := p.expect(punct, "]"); err != nil {
			return Type{}, err
		}
		if size < 1 {
			return Type{}, unknown("an array of no elements")
		}
		parsed = Type{Kind: Array, Elem: &elem, Size: size}
	}
	return parsed, nil
}

func (p *parser) number() (int, error) {
	digits, err := p.expect(number, "")
	if err != nil {
		return 0, err
	}
	value, err := strconv.Atoi(digits.text)
	if err != nil {
		return 0, unknown("a number out of range")
	}
	return value, nil
}

// parsePrimary reads a type without its suffixes.
func (p *parser) parsePrimary() (Type, error) {
	first, err := p.expect(word, "")
	if err != nil {
		return Type{}, err
	}
	switch first.text {
	case "DECIMAL":
		return p.parseDecimal()
	case "ENUM":
		return p.parseEnum()
	case "STRUCT", "UNION":
		return p.parseFields(map[string]Kind{"STRUCT": Struct, "UNION": Union}[first.text])
	case "MAP":
		return p.parseMap()
	}
	name := first.text
	// The longest base name the words that follow complete wins, so TIME WITH
	// TIME ZONE is not read as TIME followed by text.
	for _, candidate := range []string{"TIME WITH TIME ZONE", "TIMESTAMP WITH TIME ZONE"} {
		words := strings.Fields(candidate)
		if name == words[0] && p.followedBy(words[1:]) {
			p.at += len(words) - 1
			name = candidate
		}
	}
	if !slices.Contains(baseNames, name) {
		return Type{}, unknown("a type name the provider does not know")
	}
	return Type{Kind: Base, Name: name}, nil
}

// followedBy reports whether the next tokens are exactly words.
func (p *parser) followedBy(words []string) bool {
	if p.at+len(words) > len(p.tokens) {
		return false
	}
	for i, w := range words {
		if p.tokens[p.at+i].kind != word || p.tokens[p.at+i].text != w {
			return false
		}
	}
	return true
}

func (p *parser) parseDecimal() (Type, error) {
	if _, err := p.expect(punct, "("); err != nil {
		return Type{}, err
	}
	precision, err := p.number()
	if err != nil {
		return Type{}, err
	}
	if _, err := p.expect(punct, ","); err != nil {
		return Type{}, err
	}
	scale, err := p.number()
	if err != nil {
		return Type{}, err
	}
	if _, err := p.expect(punct, ")"); err != nil {
		return Type{}, err
	}
	if precision < 1 || precision > maxDecimalPrecision || scale > precision {
		return Type{}, unknown("a DECIMAL precision or scale out of range")
	}
	return Type{Kind: Decimal, Precision: precision, Scale: scale}, nil
}

func (p *parser) parseEnum() (Type, error) {
	if _, err := p.expect(punct, "("); err != nil {
		return Type{}, err
	}
	result := Type{Kind: Enum}
	for {
		label, err := p.expect(quotedLabel, "")
		if err != nil {
			return Type{}, err
		}
		result.Labels = append(result.Labels, label.text)
		if p.accept(punct, ")") {
			return result, nil
		}
		if _, err := p.expect(punct, ","); err != nil {
			return Type{}, err
		}
	}
}

func (p *parser) parseFields(kind Kind) (Type, error) {
	if _, err := p.expect(punct, "("); err != nil {
		return Type{}, err
	}
	result := Type{Kind: kind}
	for {
		next, ok := p.peek()
		if !ok || (next.kind != word && next.kind != quotedName) {
			return Type{}, unknown("a field without a name")
		}
		p.at++
		field := Field{Name: next.text, Quoted: next.kind == quotedName}
		var err error
		if field.Type, err = p.parseType(); err != nil {
			return Type{}, err
		}
		result.Fields = append(result.Fields, field)
		if p.accept(punct, ")") {
			return result, nil
		}
		if _, err := p.expect(punct, ","); err != nil {
			return Type{}, err
		}
	}
}

func (p *parser) parseMap() (Type, error) {
	if _, err := p.expect(punct, "("); err != nil {
		return Type{}, err
	}
	key, err := p.parseType()
	if err != nil {
		return Type{}, err
	}
	if _, err := p.expect(punct, ","); err != nil {
		return Type{}, err
	}
	value, err := p.parseType()
	if err != nil {
		return Type{}, err
	}
	if _, err := p.expect(punct, ")"); err != nil {
		return Type{}, err
	}
	return Type{Kind: Map, Key: &key, Value: &value}, nil
}
