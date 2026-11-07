//go:build integration && windows

package integration_test

// On Windows DuckDB names the process that holds a file and not its access
// mode, so the reason claims none.
const (
	writerHoldsReason = "has the database file open; DuckDB lets one process write it or several read it, never both"
	readerHoldsReason = writerHoldsReason
)
