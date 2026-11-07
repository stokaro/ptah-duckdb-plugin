// Package instances owns the DuckDB databases the provider process has open.
//
// DuckDB is embedded: a database is an instance inside this process, and a
// database file may be open in one process for writing, or in several for
// reading, never both (https://duckdb.org/docs/current/connect/concurrency).
// The host opens several sessions on one address during a command, so the
// registry keeps one instance per file and hands every session of that file a
// connection of its own to it. The instance closes, and DuckDB releases the
// file's lock, when the last session on it closes. An in-memory database
// lives until the process ends, so the sessions of one command see each
// other's changes, and a new process starts with an empty one.
//
// Every instance opens with the settings Settings lists: no extension is
// installed or loaded except those linked into the binary, and SQL cannot
// reach files, the network or other databases. A refusal is a *Refusal,
// whose reason the package wrote and which never quotes a path or the
// driver's text.
package instances

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/duckdb/duckdb-go/v2"
	"google.golang.org/grpc/codes"
)

// Refusal is an open the registry refuses. Code is the gRPC code the
// provider answers with, and Reason the sentence it gives, which quotes no
// path and no text of the driver.
type Refusal struct {
	Code   codes.Code
	Reason string
}

func (r *Refusal) Error() string { return r.Reason }

func refuse(code codes.Code, reason string) *Refusal { return &Refusal{Code: code, Reason: reason} }

// Registry holds the instances of one provider process. The zero value is
// ready to use, and a Registry must not be copied after first use.
type Registry struct {
	mu     sync.Mutex
	files  []*instance
	memory map[string]*instance
}

// instance is one open DuckDB database.
type instance struct {
	db     *sql.DB
	access Access
	// path is the canonical path of a file instance, and file what Stat
	// reported for it once it existed; both are empty for an in-memory one.
	path string
	file os.FileInfo
	// sessions counts the open handles of a file instance.
	sessions int
}

// Handle is one session's connection to an instance. Close it once.
type Handle struct {
	registry *Registry
	instance *instance
	conn     *sql.Conn
	once     sync.Once
}

// Conn is the session's connection. DuckDB runs one statement at a time on a
// connection, so the caller serializes its use.
func (h *Handle) Conn() *sql.Conn { return h.conn }

// Access is the access mode the instance was opened in.
func (h *Handle) Access() Access { return h.instance.access }

// Close returns the connection and closes the instance when it was the last
// session of a file. Closing twice is harmless.
func (h *Handle) Close() error {
	var err error
	h.once.Do(func() { err = h.registry.release(h) })
	return err
}

// OpenFile opens the database file at path, an absolute path, for one
// session. A file that does not exist is created unless access is ReadOnly.
func (r *Registry) OpenFile(ctx context.Context, path string, access Access) (*Handle, error) {
	if !filepath.IsAbs(path) {
		return nil, refuse(codes.InvalidArgument, "the provider opens a database file by its absolute path only")
	}
	existing, err := inspectFile(path, access)
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalPath(path, existing)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if open := r.findFile(canonical, existing); open != nil {
		if open.access != access {
			return nil, refuse(codes.FailedPrecondition, modeConflict(open.access))
		}
		return r.attach(ctx, open)
	}
	dsn, err := DSN(path, access)
	if err != nil {
		return nil, err
	}
	connector, err := duckdb.NewConnector(dsn, nil)
	if err != nil {
		return nil, classifyOpen(err, access)
	}
	opened := &instance{db: sql.OpenDB(connector), access: access, path: canonical}
	if opened.file, err = os.Stat(path); err != nil {
		_ = opened.db.Close()
		return nil, refuse(codes.Unavailable, "the database file is not there after DuckDB opened it")
	}
	handle, err := r.attach(ctx, opened)
	if err != nil {
		_ = opened.db.Close()
		return nil, err
	}
	r.files = append(r.files, opened)
	return handle, nil
}

// OpenMemory opens the in-memory database called name for one session,
// creating it on first use. It lives until the process ends.
func (r *Registry) OpenMemory(ctx context.Context, name string) (*Handle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if open := r.memory[name]; open != nil {
		return r.attach(ctx, open)
	}
	dsn, err := DSN(memoryPath, ReadWrite)
	if err != nil {
		return nil, err
	}
	connector, err := duckdb.NewConnector(dsn, nil)
	if err != nil {
		return nil, refuse(codes.Unavailable, "DuckDB could not create an in-memory database")
	}
	opened := &instance{db: sql.OpenDB(connector)}
	handle, err := r.attach(ctx, opened)
	if err != nil {
		_ = opened.db.Close()
		return nil, err
	}
	if r.memory == nil {
		r.memory = make(map[string]*instance)
	}
	r.memory[name] = opened
	return handle, nil
}

// attach takes a connection of open for a new session. The caller holds mu.
func (r *Registry) attach(ctx context.Context, open *instance) (*Handle, error) {
	conn, err := open.db.Conn(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, refuse(codes.Unavailable, "DuckDB did not give the session a connection")
	}
	open.sessions++
	return &Handle{registry: r, instance: open, conn: conn}, nil
}

// release returns a handle's connection and closes a file instance nobody
// uses any more.
func (r *Registry) release(h *Handle) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := h.conn.Close()
	h.instance.sessions--
	if h.instance.path == "" || h.instance.sessions > 0 {
		return err
	}
	for i, open := range r.files {
		if open == h.instance {
			r.files = append(r.files[:i], r.files[i+1:]...)
			break
		}
	}
	return errors.Join(err, h.instance.db.Close())
}

// findFile returns the open instance of the file at canonical, or of the
// same file under another name, such as a hard link or a spelling a
// case-insensitive filesystem folds. The caller holds mu.
func (r *Registry) findFile(canonical string, existing os.FileInfo) *instance {
	for _, open := range r.files {
		if open.path == canonical || (existing != nil && os.SameFile(open.file, existing)) {
			return open
		}
	}
	return nil
}

// modeConflict is the reason a session cannot open a file another session of
// this process has open in the other access mode.
func modeConflict(held Access) string {
	if held == ReadOnly {
		return "another session of this provider has the database file open read-only; DuckDB opens one file in one access mode per process"
	}
	return "another session of this provider has the database file open read-write; DuckDB opens one file in one access mode per process"
}

// canonicalPath names the file at path the same way whichever symbolic link
// reached it. A file that does not exist yet is named through its directory.
func canonicalPath(path string, existing os.FileInfo) (string, error) {
	target := path
	if existing == nil {
		target = filepath.Dir(path)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", refuse(codes.Unavailable, "the provider could not resolve the path of the database file")
	}
	if existing == nil {
		resolved = filepath.Join(resolved, filepath.Base(path))
	}
	return resolved, nil
}
