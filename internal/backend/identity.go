package backend

import protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"

// aliases lists the other names the engine answers to. The host catalog
// reserves none for DuckDB.
func aliases() []string { return make([]string, 0) }

// identifierRules describes how DuckDB compares names. It keeps the case a name
// is written in and compares names without regard to ASCII case. An index name
// lives in the schema, not in its table.
func identifierRules() *protocol.IdentifierRules {
	return &protocol.IdentifierRules{
		TableNames: protocol.IdentifierRules_ASCII_CASE_INSENSITIVE, ColumnNames: protocol.IdentifierRules_ASCII_CASE_INSENSITIVE,
		IndexNames: protocol.IdentifierRules_ASCII_CASE_INSENSITIVE, IndexNamespace: protocol.IdentifierRules_SCHEMA,
	}
}

// releaseLines declares no DuckDB release line. The Ptah host declares none for
// DuckDB, and a line is a claim about testing: this provider has no recipe and
// no job for any DuckDB release yet, so it states nothing about them.
func releaseLines() []*protocol.ReleaseLine { return make([]*protocol.ReleaseLine, 0) }
