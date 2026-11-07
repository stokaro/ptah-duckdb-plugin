// Package address reads the address of a DuckDB database as Ptah writes it,
// and resolves the file it names. It knows the grammar and nothing about the
// driver: what the DuckDB driver accepts is the instances package's concern.
//
// The grammar has two forms:
//
//	duckdb://<path>[?<options>]      a database file
//	duckdb://:memory:[<name>][?...]  an in-memory database of the provider process
//
// The path is everything between "duckdb://" and the first "?", with percent
// escapes decoded, so a space may be written as %20 or as itself and a "?" in
// a file name as %3F. A path that starts with "/" is absolute, so
// duckdb:///var/lib/app.duckdb names /var/lib/app.duckdb. A path that starts
// with a drive letter, such as duckdb://C:/data/app.duckdb or
// duckdb:///C:/data/app.duckdb, is an absolute Windows path. Any other path is
// relative, and Resolve joins it to the directory the caller names.
//
// The only option is access_mode, read_write (the default) or read_only.
// Parse refuses every other option, a repeated option, a fragment and an
// address that names no database. No error quotes the address or any part of
// it, because an address may carry a credential: each error is a sentence the
// package wrote.
package address

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// Scheme is the URL scheme of a DuckDB address. Parse compares it without
// regard to ASCII case.
const Scheme = "duckdb"

// MemoryPrefix starts the path of an in-memory database.
const MemoryPrefix = ":memory:"

// ErrInvalid is the error every refusal of Parse and Resolve wraps.
var ErrInvalid = errors.New("invalid DuckDB address")

// Kind says what an address names.
type Kind int

// The kinds an address names.
const (
	// File is a database file.
	File Kind = iota + 1
	// Memory is an in-memory database that lives as long as the provider
	// process.
	Memory
)

// Target is what an address names.
type Target struct {
	// Kind is File or Memory.
	Kind Kind
	// Path is the decoded path of a File target, as written: relative or
	// absolute, with the separators the address used. Resolve turns it into
	// the absolute path of the file.
	Path string
	// MemoryName names an in-memory database. The empty name is the default
	// one; every address with the same name reaches the same database while
	// the provider process runs.
	MemoryName string
	// ReadOnly is set by access_mode=read_only.
	ReadOnly bool
}

// refusal is the error of one refused address.
type refusal string

func (r refusal) Error() string { return string(r) }

func (refusal) Unwrap() error { return ErrInvalid }

// Reason is the sentence err carries when Parse or Resolve refused an address,
// and the empty string for any other error.
func Reason(err error) string {
	if r, ok := errors.AsType[refusal](err); ok {
		return string(r)
	}
	return ""
}

// accessModeOption is the option that selects read-only access.
const accessModeOption = "access_mode"

// fixedSettings are DuckDB settings the provider opens every database with.
// An address that names one is refused by name, so it cannot loosen the
// extension and file access policy.
var fixedSettings = map[string]bool{
	"allow_community_extensions":       true,
	"allow_persistent_secrets":         true,
	"allow_unsigned_extensions":        true,
	"allowed_directories":              true,
	"allowed_paths":                    true,
	"autoinstall_extension_repository": true,
	"autoinstall_known_extensions":     true,
	"autoload_known_extensions":        true,
	"custom_extension_repository":      true,
	"enable_external_access":           true,
	"extension_directory":              true,
	"lock_configuration":               true,
}

// memoryName is the grammar of an in-memory database's name.
var memoryName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Parse reads address. It does not touch the filesystem.
func Parse(address string) (Target, error) {
	scheme, rest, found := strings.Cut(address, ":")
	if !found || !strings.EqualFold(scheme, Scheme) {
		return Target{}, refusal("the address does not start with duckdb://")
	}
	rest, found = strings.CutPrefix(rest, "//")
	if !found {
		return Target{}, refusal("the address does not start with duckdb://")
	}
	if strings.Contains(rest, "#") {
		return Target{}, refusal("the address carries a fragment; write %23 for a # in a file name")
	}
	rawPath, rawQuery, _ := strings.Cut(rest, "?")
	path, err := url.PathUnescape(rawPath)
	if err != nil {
		return Target{}, refusal("the path of the address has a malformed percent escape")
	}
	target := Target{}
	if err := readOptions(rawQuery, &target); err != nil {
		return Target{}, err
	}
	if err := readPath(path, &target); err != nil {
		return Target{}, err
	}
	return target, nil
}

// readOptions reads the query of an address into target.
func readOptions(rawQuery string, target *Target) error {
	if rawQuery == "" {
		return nil
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return refusal("the options of the address are malformed")
	}
	for name, given := range values {
		switch {
		case name == accessModeOption:
		case fixedSettings[name]:
			return refusal("the address sets " + name + ", which the provider fixes for every database")
		default:
			return refusal("the address carries an option the provider does not read; it reads access_mode only")
		}
		if len(given) != 1 {
			return refusal("the address gives access_mode more than once")
		}
		switch given[0] {
		case "read_only":
			target.ReadOnly = true
		case "read_write":
		default:
			return refusal("access_mode is read_only or read_write")
		}
	}
	return nil
}

// readPath classifies the decoded path of an address.
func readPath(path string, target *Target) error {
	if strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return refusal("the path of the address holds a control character")
	}
	if name, found := strings.CutPrefix(path, MemoryPrefix); found {
		if name != "" && !memoryName.MatchString(name) {
			return refusal("an in-memory database name is 1 to 64 letters, digits, underscores and dashes")
		}
		if target.ReadOnly {
			return refusal("an in-memory database cannot be opened read-only; DuckDB refuses it")
		}
		target.Kind, target.MemoryName = Memory, name
		return nil
	}
	switch {
	case path == "":
		return refusal("the address names no database; write duckdb://<path> or duckdb://:memory:")
	case path == "/"+MemoryPrefix || strings.HasPrefix(path, "/"+MemoryPrefix):
		return refusal("the address names a file called :memory: in the root directory; write duckdb://:memory: for an in-memory database")
	case driveRelative(path):
		return refusal("the address names a path relative to the current directory of a Windows drive; give an absolute path such as C:/data/app.duckdb")
	}
	target.Kind = File
	target.Path = path
	if rooted := strings.TrimPrefix(path, "/"); rooted != path && driveAbsolute(rooted) {
		target.Path = rooted
	}
	return nil
}

// driveAbsolute reports whether path starts with a drive letter, a colon and
// a separator, as an absolute Windows path does.
func driveAbsolute(path string) bool {
	return len(path) >= 3 && isASCIILetter(path[0]) && path[1] == ':' && (path[2] == '/' || path[2] == '\\')
}

// driveRelative reports whether path starts with a drive letter and a colon
// but no separator, which Windows resolves against that drive's current
// directory.
func driveRelative(path string) bool {
	return len(path) >= 2 && isASCIILetter(path[0]) && path[1] == ':' && !driveAbsolute(path)
}

func isASCIILetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
