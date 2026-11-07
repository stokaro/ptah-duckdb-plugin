// Package migrationunits splits a migration body into the units DuckDB runs
// it as, under the contract's units.migration-split feature.
//
// Each statement is a unit, read with the provider's own lexing (see
// sqltext): from the comment before it, or its first token, through its
// semicolon, or to the end of the body for a last statement no semicolon
// ends. DuckDB runs every statement inside a transaction, schema statements
// included, and rolls it back with the transaction, so every unit runs in a
// transaction of the host's together with the checkpoint that records it,
// and the two commit together or not at all. A migration that runs without a
// transaction of its own therefore stops between two statements, never inside
// one, and a rerun resumes after the last one that committed.
//
// A statement that controls a transaction, or changes a session setting, is
// refused with the whole body: the host decides where each transaction begins
// and ends, and a setting would not reach the units after it, which may run
// on another connection.
package migrationunits

import (
	"slices"
	"strings"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"

	"github.com/stokaro/ptah-duckdb-plugin/internal/sqltext"
)

// Reasons the split gives for a refused body. None quotes the body: the host
// quotes the refused range itself.
const (
	// TransactionControlReason refuses a statement that opens or ends a
	// transaction.
	TransactionControlReason = "the statement controls a transaction, and the host decides where each transaction of a migration begins and ends; remove it"
	// SettingReason refuses a statement that changes a session setting.
	SettingReason = "the statement changes a setting, which would not reach the statements after it; remove it"
	// LexingReason refuses a body the provider cannot read into statements.
	LexingReason = "the provider cannot read the body into statements: it holds an unclosed quote or comment, an empty statement, " +
		"or a character outside DuckDB's SQL"
)

// controlWords start a statement that controls a transaction: BEGIN, START
// TRANSACTION, COMMIT, END, ROLLBACK and ABORT.
var controlWords = []string{"BEGIN", "START", "COMMIT", "END", "ROLLBACK", "ABORT"}

// settingWords start a statement that changes a setting of the session or of
// the database: SET, RESET, PRAGMA, which reads or sets one, and USE, which
// changes the default schema.
var settingWords = []string{"SET", "RESET", "PRAGMA", "USE"}

// readWords start a statement that only reads: a query, and DuckDB's FROM
// first form of one.
var readWords = []string{"SELECT", "WITH", "VALUES", "FROM", "TABLE"}

// changeWords make a query change something: a write inside a WITH, and a
// call that advances or sets a sequence.
var changeWords = []string{"INSERT", "UPDATE", "DELETE", "MERGE", "COPY", "NEXTVAL", "SETVAL"}

// Split returns the units body runs as, in order, or the refusal of the whole
// body. A body with no statement has neither.
func Split(body string) ([]*protocol.MigrationUnit, *protocol.SplitRefusal) {
	if strings.TrimSpace(body) == "" {
		return nil, nil
	}
	statements, err := sqltext.Split(body)
	if err != nil {
		return nil, refused(0, len(body), LexingReason)
	}
	units := make([]*protocol.MigrationUnit, 0, len(statements))
	for _, statement := range statements {
		switch head := statement.Words[0]; {
		case slices.Contains(controlWords, head):
			return nil, refused(statement.Start, statement.End, TransactionControlReason)
		case slices.Contains(settingWords, head):
			return nil, refused(statement.Start, statement.End, SettingReason)
		}
		source := body[statement.Start:statement.End]
		units = append(units, &protocol.MigrationUnit{
			Sql:       strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(source), ";")),
			Source:    source,
			Placement: protocol.MigrationUnit_WITH_CHECKPOINT,
		})
	}
	return units, nil
}

// ReadOnly reports whether a text, such as a check assertion, is one
// statement that reads rows and changes nothing, and why not when it is not.
func ReadOnly(text string) (bool, string) {
	statements, err := sqltext.Split(text)
	switch {
	case err != nil:
		return false, LexingReason
	case len(statements) != 1:
		return false, "the text is not exactly one statement"
	case !slices.Contains(readWords, statements[0].Words[0]):
		return false, "the statement is not a query that only reads rows"
	case slices.ContainsFunc(statements[0].Words, func(word string) bool { return slices.Contains(changeWords, word) }):
		return false, "the statement changes rows or advances a sequence"
	}
	return true, ""
}

func refused(start, end int, reason string) *protocol.SplitRefusal {
	// #nosec G115 -- the range lies in the request's body, which the transport bounds far below math.MaxUint32
	return &protocol.SplitRefusal{Start: uint32(start), End: uint32(end), Reason: reason}
}
