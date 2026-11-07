package backend

import (
	"strings"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"

	"github.com/stokaro/ptah-duckdb-plugin/internal/duckdbversion"
)

// aliases lists the other names the engine answers to. The host catalog
// reserves none for DuckDB.
func aliases() []string { return make([]string, 0) }

// identifierRules describes how DuckDB compares names, measured on the
// embedded DuckDB: a name keeps the case it is written in and compares
// without regard to ASCII case, quoted or not (a table created as "A" is read
// as a), while other letters compare exactly ("Ä" is not "ä"). An index name
// lives in its schema: CREATE INDEX IX on a second table of the schema answers
// `Index with name "IX" already exists` when ix exists on the first, and the
// same name is accepted in another schema.
func identifierRules() *protocol.IdentifierRules {
	return &protocol.IdentifierRules{
		TableNames: protocol.IdentifierRules_ASCII_CASE_INSENSITIVE, ColumnNames: protocol.IdentifierRules_ASCII_CASE_INSENSITIVE,
		IndexNames: protocol.IdentifierRules_ASCII_CASE_INSENSITIVE, IndexNamespace: protocol.IdentifierRules_SCHEMA,
	}
}

// releaseLines declares the one DuckDB release line the provider serves: the
// line of the library the executable links, certified by that link, since
// every test of the provider runs it. The line is the version's major and
// minor, so 2.0.0 is line 2.0. A host that does not read linked release lines
// receives the line as best effort.
func releaseLines() []*protocol.ReleaseLine {
	linked := strings.TrimPrefix(duckdbversion.Library, "v")
	parts := strings.SplitN(linked, ".", 3)
	return []*protocol.ReleaseLine{{
		Line: parts[0] + "." + parts[1], Support: protocol.ReleaseLine_CERTIFIED, Linked: linked,
		Note: "The provider executable links this DuckDB, and every test of the provider runs it.",
	}}
}
