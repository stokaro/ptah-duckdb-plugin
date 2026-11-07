package instances

import (
	"regexp"
	"strings"

	"google.golang.org/grpc/codes"
)

// lockHolder finds the process ID DuckDB names in a lock conflict.
var lockHolder = regexp.MustCompile(`\(PID ([0-9]{1,10})\)`)

// classifyOpen turns the driver's refusal to open a file into a refusal in the
// provider's words. The driver's text can quote the path, so it is read and
// never passed on. Each branch matches text measured on DuckDB v2.0.0:
//
//   - `Could not set lock on file "<path>": Conflicting lock is held in
//     <program> (PID <pid>) by user <user>.` when another process holds the
//     file. When that process has it open read-only and this open asks to
//     write, DuckDB adds `However, you would be able to open this database in
//     read-only mode`.
//   - `Cannot open file "<path>": Permission denied` for a file the process
//     may not write.
//   - `Corrupt database file: computed checksum ... does not match stored
//     checksum` for a file whose header block was changed.
func classifyOpen(err error, access Access) *Refusal {
	text := err.Error()
	switch {
	case strings.Contains(text, "Could not set lock on file"):
		return refuse(codes.Unavailable, lockConflict(text))
	case strings.Contains(text, "Permission denied") && access == ReadOnly:
		return refuse(codes.PermissionDenied, "the provider may not read the database file")
	case strings.Contains(text, "Permission denied"):
		return refuse(codes.PermissionDenied, "the provider may not write the database file or its directory; access_mode=read_only opens it for reading")
	case strings.Contains(text, "Corrupt database file"):
		return refuse(codes.FailedPrecondition, "the database file is corrupt: its checksum does not match its content")
	}
	return refuse(codes.Unavailable, "DuckDB could not open the database file")
}

// lockConflict is the reason for a lock another process holds.
func lockConflict(text string) string {
	holder := "another process"
	if match := lockHolder.FindStringSubmatch(text); match != nil {
		holder += " (PID " + match[1] + ")"
	}
	if strings.Contains(text, "able to open this database in read-only mode") {
		return holder + " has the database file open read-only; DuckDB lets more processes read it with access_mode=read_only, and none write it"
	}
	return holder + " has the database file open for writing; DuckDB lets no other process open it until that one closes it"
}
