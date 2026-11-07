//go:build windows

package instances

import (
	"errors"

	"golang.org/x/sys/windows"
)

// lockedByAnotherProcess reports whether err is Windows refusing a handle
// because another process holds the file: DuckDB opens a file it writes
// without sharing, and locks it against other readers.
func lockedByAnotherProcess(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
