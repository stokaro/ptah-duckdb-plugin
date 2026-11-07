package instances

import (
	"net/url"
	"regexp"
	"strings"

	"google.golang.org/grpc/codes"
)

// Setting is one DuckDB configuration option and the value the provider
// opens a database with.
type Setting struct {
	Name  string
	Value string
}

// Access is the access mode a database opens in.
type Access int

// The access modes.
const (
	// ReadWrite opens a database for reading and writing, creating a file
	// that does not exist.
	ReadWrite Access = iota
	// ReadOnly opens an existing file for reading only. DuckDB lets several
	// processes read one file, and none write it meanwhile.
	ReadOnly
)

// setting is the value of DuckDB's access_mode setting for a.
func (a Access) setting() string {
	if a == ReadOnly {
		return "read_only"
	}
	return "read_write"
}

// memoryPath is the path the driver reads as a new in-memory database.
const memoryPath = ":memory:"

// Settings lists, in a fixed order, the configuration every database opens
// with. DuckDB reads these before the database starts, and a statement cannot
// loosen them later: SET enable_external_access=true answers "Cannot enable
// external access while database is running".
//
//   - access_mode is stated, never left automatic.
//   - autoinstall_known_extensions and autoload_known_extensions are off, so
//     a statement that needs an extension the binary does not link fails
//     instead of downloading or loading one.
//   - allow_community_extensions and allow_unsigned_extensions are off.
//   - allow_persistent_secrets is off. The host starts the provider with no
//     HOME, and DuckDB then reads its secret directory relative to the working
//     directory, so a persistent secret would come from the operator's
//     project rather than from configuration.
//   - enable_external_access is off. It refuses LOAD, INSTALL, ATTACH, COPY
//     and every read or write of a file other than the database itself, which
//     is what keeps a statement from reaching the network or the filesystem.
//     It also stops DuckDB from opening a file of another format through an
//     extension. The database file, its write-ahead log and the temporary
//     directory DuckDB spills to stay usable.
func Settings(access Access) []Setting {
	return []Setting{
		{"access_mode", access.setting()},
		{"autoinstall_known_extensions", "false"},
		{"autoload_known_extensions", "false"},
		{"allow_community_extensions", "false"},
		{"allow_unsigned_extensions", "false"},
		{"allow_persistent_secrets", "false"},
		{"enable_external_access", "false"},
	}
}

// percentEscape matches a percent sign and what follows it.
var percentEscape = regexp.MustCompile(`%(..)?`)

// hexPair matches two hexadecimal digits.
var hexPair = regexp.MustCompile(`^[0-9A-Fa-f]{2}$`)

// DSN is the connection string the driver opens path with, under Settings.
// path is an absolute file path, or memoryPath.
//
// The driver takes the path as everything before the first "?" and reads the
// options by parsing the whole string as a URL. So a path with a "?" cannot
// be expressed; a "#" would turn the options into a URL fragment, which the
// driver ignores, and the database would open without the provider's
// settings; and a "%" that does not start an escape fails the parse. DSN
// refuses each of them by name.
func DSN(path string, access Access) (string, error) {
	switch {
	case strings.Contains(path, "?"):
		return "", refuse(codes.InvalidArgument, "the DuckDB driver cannot open a file whose path contains a question mark")
	case strings.Contains(path, "#"):
		return "", refuse(codes.InvalidArgument, "the DuckDB driver cannot open a file whose path contains a number sign")
	case !wellFormedPercents(path):
		return "", refuse(codes.InvalidArgument, "the DuckDB driver cannot open a file whose path has a percent sign not followed by two hexadecimal digits")
	}
	var options strings.Builder
	for i, setting := range Settings(access) {
		if i > 0 {
			options.WriteByte('&')
		}
		options.WriteString(url.QueryEscape(setting.Name))
		options.WriteByte('=')
		options.WriteString(url.QueryEscape(setting.Value))
	}
	return path + "?" + options.String(), nil
}

// wellFormedPercents reports whether every percent sign in path starts a
// two-digit hexadecimal escape.
func wellFormedPercents(path string) bool {
	for _, match := range percentEscape.FindAllStringSubmatch(path, -1) {
		if !hexPair.MatchString(match[1]) {
			return false
		}
	}
	return true
}
