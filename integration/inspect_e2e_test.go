//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	providerruntime "github.com/stokaro/ptah/provider-sdk/runtime"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// startReader starts the provider for a host that lists features.
func startReader(c *qt.C, features []string) *providerruntime.Client {
	c.Helper()
	binary, digest := providerBinary(c)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := providerruntime.Start(ctx, providerruntime.Options{
		Executable: binary, SHA256: digest, ProviderID: backend.ProviderID, HostFeatures: features, StartupTimeout: 60 * time.Second,
	})
	c.Assert(err, qt.IsNil)
	c.Cleanup(client.Close)
	return client
}

// The executable's read passes the checks the SDK's client runs on every
// snapshot, and two reads of an unchanged database are one.
func TestProviderInspectsADatabaseFile(t *testing.T) {
	c := testcheck.New(t)
	path := seedFile(c, "rich.duckdb", richSchema)
	client := startReader(c, readerFeatures())
	session, err := client.Open(c.Context(), openRequest(fileAddress(path)+"?access_mode=read_only"))
	c.Assert(err, qt.IsNil)
	first, err := client.Inspect(c.Context(), &protocol.InspectRequest{SessionId: session.GetId()})
	c.Assert(err, qt.IsNil)
	second, err := client.Inspect(c.Context(), &protocol.InspectRequest{SessionId: session.GetId(), Schemas: []string{"main"}})
	c.Assert(err, qt.IsNil)
	c.Assert(first.GetTables(), qt.HasLen, 2)
	c.Assert(first.GetCommonObjects().GetViews(), qt.HasLen, 1)
	c.Assert(first.GetCommonObjects().GetSequences(), qt.HasLen, 1)
	c.Assert(first.GetCommonObjects().GetComments(), qt.HasLen, 5)
	c.Assert(second.GetTables(), qt.HasLen, 2)
	c.Assert(second.GetScopeBindings(), qt.HasLen, 1)
	c.Assert(client.CloseSession(c.Context(), session.GetId()), qt.IsNil)
}

// A refused read reaches the host with the provider's reason.
func TestProviderRefusesARead(t *testing.T) {
	c := testcheck.New(t)
	path := seedFile(c, "macro.duckdb", []string{"CREATE MACRO add1(a) AS a + 1"})
	client := startReader(c, readerFeatures())
	session, err := client.Open(c.Context(), openRequest(fileAddress(path)))
	c.Assert(err, qt.IsNil)
	snapshot, err := client.Inspect(c.Context(), &protocol.InspectRequest{SessionId: session.GetId()})
	c.Assert(err, qt.ErrorMatches, `provider inspect failed \(FailedPrecondition\): schema "main" holds macro "add1", which the provider does not read`)
	c.Assert(snapshot, qt.IsNil)
}
