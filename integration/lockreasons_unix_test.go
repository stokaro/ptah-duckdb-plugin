//go:build integration && !windows

package integration_test

// DuckDB names the access mode of the process that holds a file outside
// Windows: a holder that only reads gets a sentence of its own.
const (
	writerHoldsReason = "has the database file open for writing; DuckDB lets no other process open it until that one closes it"
	readerHoldsReason = "has the database file open read-only; DuckDB lets more processes read it with access_mode=read_only, and none write it"
)
