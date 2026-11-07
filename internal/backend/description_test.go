package backend_test

import (
	"testing"

	qt "github.com/frankban/quicktest"
	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"github.com/stokaro/ptah/provider-sdk/provider"
	"github.com/stokaro/ptah/provider-sdk/runtime"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

func TestDescription_Validates(t *testing.T) {
	c := testcheck.New(t)
	c.Assert(provider.ValidateDescription(backend.Description(), nil), qt.IsNil)
}

func TestDescription_DeclaresTheEngine(t *testing.T) {
	c := testcheck.New(t)
	description := backend.Description()
	c.Assert(description.GetProviderId(), qt.Equals, "github.com/stokaro/ptah-duckdb-plugin")
	c.Assert(description.GetSemanticIdentity(), qt.Equals, "duckdb-1")
	c.Assert(description.GetEngines(), qt.HasLen, 1)
	engine := description.GetEngines()[0]
	c.Assert(engine.GetId(), qt.Equals, "duckdb")
	c.Assert(engine.GetAliases(), qt.DeepEquals, make([]string, 0))
	c.Assert(engine.GetDefaultSchema(), qt.Equals, "main")
	c.Assert(engine.GetIdentifiers().GetTableNames(), qt.Equals, protocol.IdentifierRules_ASCII_CASE_INSENSITIVE)
	c.Assert(engine.GetIdentifiers().GetColumnNames(), qt.Equals, protocol.IdentifierRules_ASCII_CASE_INSENSITIVE)
	c.Assert(engine.GetIdentifiers().GetIndexNames(), qt.Equals, protocol.IdentifierRules_ASCII_CASE_INSENSITIVE)
	c.Assert(engine.GetIdentifiers().GetIndexNamespace(), qt.Equals, protocol.IdentifierRules_SCHEMA)
	c.Assert(engine.GetReleaseLines(), qt.HasLen, 0)
}

// The provider serves sessions and nothing else, so the only feature it lists
// is the reasons its refusals carry.
func TestDescription_ListsTheSessionFeaturesOnly(t *testing.T) {
	c := testcheck.New(t)
	description := backend.Description()
	c.Assert(description.GetFeatures(), qt.DeepEquals, []string{provider.ErrorReasonsFeature})
	c.Assert(description.GetEngines()[0].GetFeatures(), qt.DeepEquals, []string{provider.ErrorReasonsFeature})
}

func TestDescription_NeedsTheContractItIsBuiltWith(t *testing.T) {
	c := testcheck.New(t)
	minimum := provider.MinimumContract(backend.Description())
	c.Assert(minimum.GetMajor(), qt.Equals, provider.Revision)
	c.Assert(minimum.GetMinor(), qt.Equals, provider.ContractMinor)
}

func TestServer_ValidatesWithTheSessionServiceOnly(t *testing.T) {
	c := testcheck.New(t)
	server := runtime.Server{Description: backend.Description(), Sessions: backend.New()}
	c.Assert(server.Validate(), qt.IsNil)
}

func TestServer_RefusesAServiceTheDescriptionDoesNotAdvertise(t *testing.T) {
	c := testcheck.New(t)
	description := backend.Description()
	description.Features = append(description.Features, provider.InspectFeature)
	server := runtime.Server{Description: description, Sessions: backend.New()}
	c.Assert(server.Validate(), qt.ErrorIs, provider.ErrProtocol)
}
