package devclean

import (
	"slices"
	"strings"

	"github.com/stokaro/ptah-duckdb-plugin/internal/sqltext"
)

// Replay judges one statement a migration replay runs on a dev database. It
// returns the empty string for a statement whose effects stay in the dev
// database, where a cleanup removes them, and otherwise the operation it
// refuses, in one line that quotes nothing from the statement.
//
// The judgment accepts what it knows and refuses the rest. Schemas, tables,
// views, indexes, sequences, macros and types are the objects a cleanup
// removes, and a row lives in a table, so creating, changing and dropping
// them, commenting on them and reading and writing rows is accepted. A
// temporary object, a setting, a transaction left open and a prepared
// statement belong to the session and reach the statements the run sends
// after it; another database, an extension, a secret and a file outlive a
// cleanup. Each is refused, and so is a statement the provider cannot read.
func Replay(statement string) string {
	statements, err := sqltext.Split(statement)
	switch {
	case err != nil:
		return "a statement the provider cannot read"
	case len(statements) != 1:
		return "more than one statement in one replay statement"
	}
	words := statements[0].Words
	if reason := catalogReference(words); reason != "" {
		return reason
	}
	first := words[0]
	if first == "UPDATE" && len(words) > 1 && words[1] == "EXTENSIONS" {
		return "UPDATE EXTENSIONS installs or loads an extension"
	}
	switch first {
	case "SELECT", "WITH", "VALUES", "FROM", "TABLE", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "COMMENT", "EXPLAIN", "DESCRIBE",
		"SUMMARIZE", "SHOW", "ANALYZE", "CHECKPOINT":
		return ""
	case "CREATE", "ALTER", "DROP":
		return objectStatement(first, words[1:])
	case "SET", "RESET", "PRAGMA", "USE":
		return first + " changes a setting of the session or the database"
	case "BEGIN", "START", "COMMIT", "END", "ROLLBACK", "ABORT":
		return first + " changes the state of the session's transaction"
	case "PREPARE", "EXECUTE", "DEALLOCATE":
		return first + " changes the session's prepared statements"
	case "ATTACH", "DETACH":
		return first + " reaches another database"
	case "INSTALL", "LOAD", "FORCE":
		return first + " installs or loads an extension"
	case "COPY", "EXPORT", "IMPORT":
		return first + " reads or writes a file outside the database"
	}
	return first + " statement"
}

// confinedObjects are the objects after CREATE, ALTER or DROP that a cleanup
// removes.
var confinedObjects = []string{"TABLE", "VIEW", "INDEX", "SEQUENCE", "SCHEMA", "MACRO", "FUNCTION", "TYPE"}

// objectStatement judges CREATE, ALTER or DROP by the object the words after
// it name: one a cleanup removes is accepted, unless the creation is
// temporary, which the session's catalog keeps.
func objectStatement(verb string, words []string) string {
	for len(words) > 0 && slices.Contains([]string{"OR", "REPLACE", "UNIQUE"}, words[0]) {
		words = words[1:]
	}
	switch {
	case len(words) == 0:
		return verb + " statement"
	case words[0] == "TEMP" || words[0] == "TEMPORARY":
		return verb + " " + words[0] + " creates an object of the session"
	case words[0] == "SECRET" || words[0] == "PERSISTENT":
		return verb + " " + words[0] + " changes a secret, which outlives the database"
	case slices.Contains(confinedObjects, words[0]):
		return ""
	}
	return verb + " " + words[0]
}

// catalogReference refuses a name qualified with the session's temporary
// catalog or the system catalog, whose objects a cleanup of the database
// does not reach.
func catalogReference(words []string) string {
	for i := 0; i+1 < len(words); i++ {
		if (words[i] == "TEMP" || words[i] == "SYSTEM") && words[i+1] == "." && (i == 0 || words[i-1] != ".") {
			return "a name in the " + strings.ToLower(words[i]) + " catalog, which a cleanup of the database does not reach"
		}
	}
	return ""
}
