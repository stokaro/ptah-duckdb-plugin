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
// of every object the provider describes under every host profile, the
// transactions and the locks.
//
// DuckDB lets one process write a file, so the lock check runs in process:
// a second session of the provider process that holds the lock must find it
// busy, and a second process must fail to open the database at all.
//
// One check fails, by the contract rather than by the provider. The
// provider's only planning service owns the engine's tables, so under a host
// that lists no two-way feature the negotiated description has no planning
// service, and the SDK refuses that host. The suite accepts such a refusal
// from a host that leaves out one feature, and counts it as a failure from a
// host that lists none. The failure is pinned here so that any other one, or
// a change of this one, is seen.
func TestConformance(t *testing.T) {
	c := testcheck.New(t)
	binary, digest := providerBinary(c)
	report, err := conformance.Check(c.Context(), conformance.Config{
		Target:     conformance.Executable(binary, digest),
		ProviderID: backend.ProviderID,
		Live:       &conformance.Live{Address: fileAddress(seedFile(c, "conformance.duckdb", richSchema)), InProcessLocks: true},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(failures(report), qt.DeepEquals, map[string]string{
		"description.valid": "Describe under a host that lists no two-way feature failed: " +
			"FailedPrecondition: invalid provider protocol exchange: missing or duplicate planning service",
	})
	statuses := checkStatuses(report)
	for _, id := range []string{"negotiation.description", "live.open", "live.inspect", "live.locks", "live.transactions"} {
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
