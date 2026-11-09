//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/duckdb/duckdb-go/v2"
	qt "github.com/frankban/quicktest"
	protocol "ptah.run/provider-sdk/protocol/experimental"
	"ptah.run/provider-sdk/provider"
	providerruntime "ptah.run/provider-sdk/runtime"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
)

// providerBuild is the provider binary this test process compiled with the
// race detector, in a directory TestMain removes, or the reason it could not
// be built. A build per test would link the DuckDB library again each time,
// and a failed build would be retried by every test after it.
var providerBuild struct {
	sync.Mutex
	done                                 bool
	source, dir, binary, digest, failure string
}

func TestMain(m *testing.M) {
	// The source directory is resolved before any test changes the working
	// directory, as the relative-path tests do.
	source, err := filepath.Abs(filepath.Join("..", "cmd", "ptah-plugin-duckdb"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	providerBuild.source = source
	code := m.Run()
	providerBuild.Lock()
	if providerBuild.dir != "" {
		_ = os.RemoveAll(providerBuild.dir)
	}
	providerBuild.Unlock()
	os.Exit(code)
}

// binaryVariable names a provider executable the end-to-end tests start
// instead of building one. The release job sets it to the binary its archive
// carries, so the tests run against the shipped build.
const binaryVariable = "PTAH_DUCKDB_PROVIDER_BIN"

// providerBinary returns the provider built with the race detector, or the
// one binaryVariable names, and its digest.
func providerBinary(c *qt.C) (binary, digest string) {
	c.Helper()
	providerBuild.Lock()
	defer providerBuild.Unlock()
	if !providerBuild.done {
		providerBuild.failure = buildProvider()
		providerBuild.done = true
	}
	c.Assert(providerBuild.failure, qt.Equals, "")
	return providerBuild.binary, providerBuild.digest
}

// buildProvider compiles the provider and returns why it could not, or the
// empty string. The caller holds providerBuild.
func buildProvider() string {
	if given := os.Getenv(binaryVariable); given != "" {
		return digestProvider(given)
	}
	dir, err := os.MkdirTemp("", "ptah-plugin-duckdb-build-")
	if err != nil {
		return err.Error()
	}
	providerBuild.dir = dir
	name := "ptah-plugin-duckdb"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(dir, name)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	command := testCommand(ctx, "go", "build", "-race", "-o", binary, providerBuild.source)
	command.Dir = providerBuild.source
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("build provider: %v: %s", err, output)
	}
	return digestProvider(binary)
}

// digestProvider records binary, which must be absolute, and its digest. The
// caller holds providerBuild.
func digestProvider(binary string) string {
	if !filepath.IsAbs(binary) {
		return fmt.Sprintf("the provider binary %s is not an absolute path", binary)
	}
	contents, err := os.ReadFile(binary) // #nosec G304 G703 -- the binary this test process built, or the one the release job names
	if err != nil {
		return err.Error()
	}
	sum := sha256.Sum256(contents)
	providerBuild.binary, providerBuild.digest = binary, hex.EncodeToString(sum[:])
	return ""
}

func testCommand(ctx context.Context, binary string, args ...string) *exec.Cmd {
	// #nosec G204 -- binary is the go tool, and args are the test's own flags and paths; exec passes them as argv, not through a shell
	return exec.CommandContext(ctx, binary, args...)
}

// startProvider starts the provider as a host does: by its digest, with no
// environment of its own and in the test process's working directory. The
// host lists errors.reasons, so a refusal carries the provider's reason, and
// planning.table-ownership, which a host must list to start the provider.
func startProvider(c *qt.C) *providerruntime.Client {
	c.Helper()
	binary, digest := providerBinary(c)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := providerruntime.Start(ctx, providerruntime.Options{
		Executable: binary, SHA256: digest, ProviderID: backend.ProviderID,
		HostFeatures: []string{provider.ErrorReasonsFeature, provider.TableOwnershipFeature}, StartupTimeout: 60 * time.Second,
	})
	c.Assert(err, qt.IsNil)
	c.Cleanup(client.Close)
	return client
}

// fileAddress is the address of the database file at path, an absolute
// path, in the form every platform reads.
func fileAddress(path string) string {
	return "duckdb://" + filepath.ToSlash(path)
}

// openRequest opens address on the duckdb engine.
func openRequest(address string) *protocol.OpenRequest {
	return &protocol.OpenRequest{Engine: "duckdb", Address: address}
}

// openWriter opens the database file at path for writing in the test
// process, outside the provider, as another program would. It is closed when
// the test ends.
func openWriter(c *qt.C, path string) *sql.DB {
	c.Helper()
	connector, err := duckdb.NewConnector(path+"?access_mode=read_write", nil)
	c.Assert(err, qt.IsNil)
	db := sql.OpenDB(connector)
	c.Cleanup(func() { _ = db.Close() })
	return db
}

// createDatabase creates a DuckDB database file at a new path in a temporary
// directory, with one table, closes it and returns the path.
func createDatabase(c *qt.C, name string) string {
	c.Helper()
	path := filepath.Join(c.TempDir(), name)
	connector, err := duckdb.NewConnector(path, nil)
	c.Assert(err, qt.IsNil)
	db := sql.OpenDB(connector)
	_, err = db.ExecContext(c.Context(), "CREATE TABLE seeded(id INTEGER)")
	c.Assert(err, qt.IsNil)
	c.Assert(db.Close(), qt.IsNil)
	return path
}
