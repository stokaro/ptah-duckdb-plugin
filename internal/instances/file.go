package instances

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"google.golang.org/grpc/codes"
)

// duckdbMagic sits at headerMagicOffset of every DuckDB database file, after
// the checksum of the header block.
var duckdbMagic = []byte("DUCK")

const headerMagicOffset = 8

// sqliteMagic starts every SQLite database file. DuckDB answers such a file
// by loading its sqlite extension, which the provider never lets it do, so
// the file is refused before DuckDB sees it.
var sqliteMagic = []byte("SQLite format 3\x00")

// statFile checks what is at path before DuckDB opens it, so a refusal can
// say what is wrong in the provider's words rather than the driver's. It
// returns what Stat reports for an existing file, and nil for one the open
// will create. It does not read the file; checkHeader does.
func statFile(path string, access Access) (os.FileInfo, error) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, inspectMissing(path, access)
	case errors.Is(err, syscall.ENOTDIR):
		return nil, refuse(codes.InvalidArgument, "the database file would be created inside something that is not a directory")
	case errors.Is(err, fs.ErrPermission):
		return nil, refuse(codes.PermissionDenied, "the provider may not look up the database file")
	case err != nil:
		return nil, refuse(codes.Unavailable, "the provider could not look up the database file")
	case info.IsDir():
		return nil, refuse(codes.InvalidArgument, "the address names a directory, not a database file")
	}
	return info, nil
}

// inspectMissing decides whether a file that is not there may be created.
func inspectMissing(path string, access Access) error {
	if access == ReadOnly {
		return refuse(codes.NotFound, "the database file does not exist, and a read-only open does not create it")
	}
	dir, err := os.Stat(filepath.Dir(path))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return refuse(codes.NotFound, "the directory that would hold the database file does not exist")
	case err != nil:
		return refuse(codes.Unavailable, "the provider could not look up the directory that would hold the database file")
	case !dir.IsDir():
		return refuse(codes.InvalidArgument, "the database file would be created inside something that is not a directory")
	}
	return nil
}

// checkHeader refuses a file that is not a DuckDB database. A file another
// process holds locked against reading, as DuckDB's lock does on Windows, is
// left to DuckDB, whose refusal names the process that holds it: a file
// nobody can read cannot be opened as a SQLite file either.
func checkHeader(path string) error {
	file, err := os.Open(path) // #nosec G304 -- the operator's address names the database file the provider opens
	switch {
	case lockedByAnotherProcess(err):
		return nil
	case errors.Is(err, fs.ErrPermission):
		return refuse(codes.PermissionDenied, "the provider may not read the database file")
	case err != nil:
		return refuse(codes.Unavailable, "the provider could not read the database file")
	}
	defer func() { _ = file.Close() }()
	header := make([]byte, len(sqliteMagic))
	n, err := io.ReadFull(file, header)
	switch {
	case lockedByAnotherProcess(err):
		return nil
	case err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF):
		return refuse(codes.Unavailable, "the provider could not read the database file")
	}
	header = header[:n]
	switch {
	case n == 0:
		return refuse(codes.FailedPrecondition, "the file is empty, so it is not a DuckDB database")
	case bytes.Equal(header, sqliteMagic):
		return refuse(codes.FailedPrecondition, "the file is a SQLite database, not a DuckDB database")
	case n < headerMagicOffset+len(duckdbMagic) || !bytes.Equal(header[headerMagicOffset:headerMagicOffset+len(duckdbMagic)], duckdbMagic):
		return refuse(codes.FailedPrecondition, "the file is not a DuckDB database")
	}
	return nil
}
