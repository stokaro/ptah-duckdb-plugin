package backend

import protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"

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

// releaseLines declares no DuckDB release line. The provider embeds one
// DuckDB, which every test runs against, but the contract lets a line claim
// testing only with an image that runs it, and an embedded engine has none: a
// line without an image may only be best effort, which would understate what
// the tests run. The gap is recorded in the README.
func releaseLines() []*protocol.ReleaseLine { return make([]*protocol.ReleaseLine, 0) }
