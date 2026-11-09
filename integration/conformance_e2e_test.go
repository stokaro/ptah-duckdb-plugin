//go:build integration

package integration_test

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"ptah.run/provider-sdk/conformance"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// The SDK's conformance suite checks the executable as a strict host: the
// handshake, the description under every host profile, every RPC, and with a
// database file the sessions, including a restart of the process, the read
// of every object the provider describes under every host profile, the
// transactions and the locks.
//
// DuckDB lets one process write a file, so the lock check runs in process:
// a second session of the provider process that holds the lock must find it
// busy, and a second process must fail to open the database at all.
//
// The provider's only planning service owns the engine's tables, so under a
// host that lists no two-way feature, and under a contract 25.0 host, the
// negotiated description has no planning service and the SDK refuses that
// host. The suite counts that refusal as the answer negotiation asks for, as
// it does for a host that leaves out one feature, so every check passes.
func TestConformance(t *testing.T) {
	c := testcheck.New(t)
	binary, digest := providerBinary(c)
	report, err := conformance.Check(c.Context(), conformance.Config{
		Target:     conformance.Executable(binary, digest),
		ProviderID: backend.ProviderID,
		Live:       &conformance.Live{Address: fileAddress(seedFile(c, "conformance.duckdb", richSchema)), InProcessLocks: true},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(failures(report), qt.DeepEquals, make(map[string]string))
	statuses := checkStatuses(report)
	for _, id := range []string{
		"description.valid", "handshake.contract-25.0-host", "negotiation.description", "live.open", "live.inspect", "live.locks",
		"live.transactions",
	} {
		c.Assert(statuses[id], qt.Equals, conformance.Passed, qt.Commentf("check %s", id))
	}
}

// failures are the details of the checks a report failed, by check.
func failures(report *conformance.Report) map[string]string {
	failed := make(map[string]string)
	for _, result := range report.Results {
		if result.Status == conformance.Failed {
			failed[result.ID] = result.Detail
		}
	}
	return failed
}

// checkStatuses are the outcomes of a report's checks, by check.
func checkStatuses(report *conformance.Report) map[string]conformance.Status {
	statuses := make(map[string]conformance.Status, len(report.Results))
	for _, result := range report.Results {
		statuses[result.ID] = result.Status
	}
	return statuses
}
