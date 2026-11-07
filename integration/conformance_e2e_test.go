//go:build integration

package integration_test

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/stokaro/ptah/provider-sdk/conformance"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// The SDK's conformance suite checks the executable as a strict host: the
// handshake, the description under every host profile, every RPC, and with a
// database file the sessions, including a restart of the process, the read
// of every object the provider describes under every host profile, and the
// transactions.
//
// live.locks fails, and the test holds the reason: the check opens the
// database from a second process to take the lock against the first, and
// DuckDB lets no second process open a file one process writes. That refusal
// is the cross-process exclusion the lock exists for; TestLocks covers the
// sessions of one process, which share the provider's lock table.
func TestConformance(t *testing.T) {
	c := testcheck.New(t)
	binary, digest := providerBinary(c)
	report, err := conformance.Check(c.Context(), conformance.Config{
		Target:     conformance.Executable(binary, digest),
		ProviderID: backend.ProviderID,
		Live:       &conformance.Live{Address: fileAddress(seedFile(c, "conformance.duckdb", richSchema))},
	})
	c.Assert(err, qt.IsNil)
	failures := report.Failures()
	c.Assert(failures, qt.HasLen, 1, qt.Commentf("%v", report.Err()))
	c.Assert(failures[0].ID, qt.Equals, "live.locks")
	c.Assert(failures[0].Detail, qt.Matches, `a session of a second process: Open refused the address: Unavailable: another process \(PID [0-9]+\) `+
		`has the database file open for writing; DuckDB lets no other process open it until that one closes it`)
}
