package backend_test

import (
	"testing"

	qt "github.com/frankban/quicktest"
	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"github.com/stokaro/ptah/provider-sdk/provider"

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

// The provider reads schemas and plans nothing yet, so it lists the read and
// what a read carries.
func TestDescription_ListsTheReadFeatures(t *testing.T) {
	c := testcheck.New(t)
	description := backend.Description()
	want := []string{
		provider.InspectFeature, provider.ErrorReasonsFeature,
		provider.ObservedColumnsFeature, provider.ObservedPrimaryKeysFeature, provider.ObservedConstraintsFeature, provider.ObservedIndexesFeature,
		provider.NormalizeValuesFeature, provider.CommonObjectsFeature,
	}
	c.Assert(description.GetFeatures(), qt.DeepEquals, want)
	c.Assert(description.GetEngines()[0].GetFeatures(), qt.DeepEquals, want)
	c.Assert(description.GetEngines()[0].GetCommonObjects().GetKinds(), qt.DeepEquals, []protocol.CommonObjectSupport_Kind{
		protocol.CommonObjectSupport_VIEW, protocol.CommonObjectSupport_SEQUENCE, protocol.CommonObjectSupport_COMMENT,
	})
}

func TestDescription_NeedsTheContractItIsBuiltWith(t *testing.T) {
	c := testcheck.New(t)
	minimum := provider.MinimumContract(backend.Description())
	c.Assert(minimum.GetMajor(), qt.Equals, provider.Revision)
	c.Assert(minimum.GetMinor(), qt.Equals, provider.ContractMinor)
}

func TestServer_Validates(t *testing.T) {
	c := testcheck.New(t)
	c.Assert(backend.Server().Validate(), qt.IsNil)
}

func TestServer_RefusesAServiceTheDescriptionDoesNotAdvertise(t *testing.T) {
	c := testcheck.New(t)
	server := backend.Server()
	server.Description.Features = append(server.Description.Features, provider.RenderFeature)
	c.Assert(server.Validate(), qt.ErrorIs, provider.ErrProtocol)
}
