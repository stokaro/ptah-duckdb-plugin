package backend

import protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"

// unprobedReason is why every line is best effort. The scaffold has no launch
// recipe and no test job for any DuckDB release.
const unprobedReason = "The provider is a scaffold: it has no launch recipe for this line and no job that runs it."

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

// releaseLines declares the DuckDB release lines, newest first. The Ptah host
// declares none for DuckDB, so these follow the lines the DuckDB project
// publishes. DuckDB is an embedded library, so no line names an image.
func releaseLines() []*protocol.ReleaseLine {
	bestEffort := func(line string) *protocol.ReleaseLine {
		return &protocol.ReleaseLine{Line: line, Label: "DuckDB " + line, Support: protocol.ReleaseLine_BEST_EFFORT,
			Unprobed: unprobedReason}
	}
	return []*protocol.ReleaseLine{
		bestEffort("2.0"),
		bestEffort("1.5"),
		bestEffort("1.4"),
	}
}
