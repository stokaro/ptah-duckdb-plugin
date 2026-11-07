//go:build integration

package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/duckdbversion"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// The host starts the provider in its own working directory and gives it no
// environment, so a relative path names a file beside the operator's
// project, never beside the provider's executable.
func TestProviderResolvesARelativePathAgainstTheHostsDirectory(t *testing.T) {
	c := testcheck.New(t)
	binary, _ := providerBinary(c)
	dir := c.TempDir()
	c.Assert(os.Mkdir(filepath.Join(dir, "data"), 0o750), qt.IsNil)
	t.Chdir(dir)
	client := startProvider(c)
	session, err := client.Open(c.Context(), openRequest("duckdb://data/rel.duckdb"))
	c.Assert(err, qt.IsNil)
	c.Assert(session.GetDatabase(), qt.Equals, "rel")
	c.Assert(session.GetServerVersion(), qt.Equals, duckdbversion.Library)
	_, err = os.Stat(filepath.Join(dir, "data", "rel.duckdb"))
	c.Assert(err, qt.IsNil)
	_, err = os.Stat(filepath.Join(filepath.Dir(binary), "data", "rel.duckdb"))
	c.Assert(err, qt.ErrorIs, os.ErrNotExist)
	c.Assert(client.CloseSession(c.Context(), session.GetId()), qt.IsNil)
}

// Two provider processes cannot both write one file. The second is refused
// with a reason that names the lock, and opens once the first session closes,
// without the first process ending.
func TestProvidersCompeteForAFile(t *testing.T) {
	c := testcheck.New(t)
	path := createDatabase(c, "contested.duckdb")
	first, second := startProvider(c), startProvider(c)
	held, err := first.Open(c.Context(), openRequest(fileAddress(path)))
	c.Assert(err, qt.IsNil)

	session, err := second.Open(c.Context(), openRequest(fileAddress(path)))
	c.Assert(err, qt.ErrorMatches, `provider open session failed \(Unavailable\): another process \(PID [0-9]+\) has the database file open for writing; `+
		`DuckDB lets no other process open it until that one closes it`)
	c.Assert(session, qt.IsNil)
	session, err = second.Open(c.Context(), openRequest(fileAddress(path)+"?access_mode=read_only"))
	c.Assert(err, qt.ErrorMatches, `provider open session failed \(Unavailable\): another process \(PID [0-9]+\) has the database file open for writing; .*`)
	c.Assert(session, qt.IsNil)

	c.Assert(first.CloseSession(c.Context(), held.GetId()), qt.IsNil)
	session, err = second.Open(c.Context(), openRequest(fileAddress(path)))
	c.Assert(err, qt.IsNil)
	c.Assert(second.CloseSession(c.Context(), session.GetId()), qt.IsNil)
}

// Several provider processes read one file together, and none of them can
// then write it.
func TestProvidersShareAReadOnlyFile(t *testing.T) {
	c := testcheck.New(t)
	path := createDatabase(c, "shared.duckdb")
	readOnly := fileAddress(path) + "?access_mode=read_only"
	first, second, third := startProvider(c), startProvider(c), startProvider(c)
	_, err := first.Open(c.Context(), openRequest(readOnly))
	c.Assert(err, qt.IsNil)
	_, err = second.Open(c.Context(), openRequest(readOnly))
	c.Assert(err, qt.IsNil)
	session, err := third.Open(c.Context(), openRequest(fileAddress(path)))
	c.Assert(err, qt.ErrorMatches, `provider open session failed \(Unavailable\): another process \(PID [0-9]+\) has the database file open read-only; `+
		`DuckDB lets more processes read it with access_mode=read_only, and none write it`)
	c.Assert(session, qt.IsNil)
}

// A program outside Ptah that writes the file keeps the provider out, and the
// reason names that program's process: here the test process itself.
func TestProviderReportsAnExternalWriter(t *testing.T) {
	c := testcheck.New(t)
	path := createDatabase(c, "external.duckdb")
	writer := openWriter(c, path)
	c.Assert(writer.PingContext(c.Context()), qt.IsNil)
	client := startProvider(c)
	session, err := client.Open(c.Context(), openRequest(fileAddress(path)))
	c.Assert(err, qt.ErrorMatches, fmt.Sprintf(`provider open session failed \(Unavailable\): another process \(PID %s\) has the database file open for writing; .*`,
		strconv.Itoa(os.Getpid())))
	c.Assert(session, qt.IsNil)
}

// A refusal reaches the host with the provider's reason, and quotes no part
// of the address.
func TestProviderRefusalQuotesNoPath(t *testing.T) {
	c := testcheck.New(t)
	missing := filepath.Join(c.TempDir(), "private-name.duckdb")
	client := startProvider(c)
	session, err := client.Open(c.Context(), openRequest(fileAddress(missing)+"?access_mode=read_only"))
	c.Assert(err, qt.ErrorMatches, `provider open session failed \(NotFound\): the database file does not exist, and a read-only open does not create it`)
	c.Assert(err.Error(), qt.Not(qt.Contains), "private-name")
	c.Assert(session, qt.IsNil)
}
