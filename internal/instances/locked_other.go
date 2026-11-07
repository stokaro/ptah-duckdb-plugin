//go:build !windows

package instances

// lockedByAnotherProcess reports whether err is the system refusing a handle
// because another process holds the file. DuckDB's locks are advisory outside
// Windows, so reading a held file never fails for that reason.
func lockedByAnotherProcess(error) bool { return false }
