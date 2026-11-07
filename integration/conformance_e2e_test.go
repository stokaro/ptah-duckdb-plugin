//go:build integration

package integration_test

import (
	"path/filepath"
	"testing"

	"github.com/stokaro/ptah/provider-sdk/conformance"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// The SDK's conformance suite checks the executable as a strict host: the
// handshake, the description under every host profile, every RPC, and with a
// database file the sessions, including a restart of the process.
func TestConformance(t *testing.T) {
	c := testcheck.New(t)
	binary, digest := providerBinary(c)
	conformance.Run(t, conformance.Config{
		Target:     conformance.Executable(binary, digest),
		ProviderID: backend.ProviderID,
		Live:       &conformance.Live{Address: fileAddress(filepath.Join(c.TempDir(), "conformance.duckdb"))},
	})
}
