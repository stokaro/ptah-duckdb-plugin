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
}

// The one release line is the linked DuckDB's, certified by the link; a host
// that does not read linked lines gets it as best effort, with the version.
func TestDescription_DeclaresTheLinkedReleaseLine(t *testing.T) {
	c := testcheck.New(t)
	lines := backend.Description().GetEngines()[0].GetReleaseLines()
	c.Assert(lines, qt.HasLen, 1)
	c.Assert(lines[0].GetLine(), qt.Equals, "2.0")
	c.Assert(lines[0].GetSupport(), qt.Equals, protocol.ReleaseLine_CERTIFIED)
	c.Assert(lines[0].GetLinked(), qt.Equals, "2.0.0")
	c.Assert(lines[0].GetImage(), qt.Equals, "")
	c.Assert(lines[0].GetNote(), qt.Equals, "The provider executable links this DuckDB, and every test of the provider runs it.")
	older := provider.NegotiatedDescription(backend.Description(), nil).GetEngines()[0].GetReleaseLines()
	c.Assert(older, qt.HasLen, 1)
	c.Assert(older[0].GetSupport(), qt.Equals, protocol.ReleaseLine_BEST_EFFORT)
	c.Assert(older[0].GetLinked(), qt.Equals, "")
	c.Assert(older[0].GetUnprobed(), qt.Equals, "the provider runs the engine it links, version 2.0.0, which this host reads no evidence of")
}

// The provider owns its tables' lifecycle and keeps the migrator's records,
// so it lists the read, the planning services, the execution, the history
// and the dev database services beside what a read carries.
func TestDescription_ListsTheFeatures(t *testing.T) {
	c := testcheck.New(t)
	description := backend.Description()
	want := []string{
		provider.InspectFeature, provider.RenderFeature, provider.ExecuteFeature, provider.AnalyzeSQLFeature, provider.ErrorReasonsFeature,
		provider.ValidateSchemaFeature, provider.PrepareTablesFeature, provider.ProjectTableCreationsFeature, provider.ColumnsFeature,
		provider.PlanFeaturesFeature, provider.PlanningLifecycleFeature, provider.PrimaryKeyChangesFeature, provider.ConstraintChangesFeature,
		provider.CheckEquivalenceFeature, provider.PlanningPolicyFeature, provider.PlanningStatementStyleFeature, provider.LocksFeature,
		provider.TransactionsFeature, provider.HistoryFeature, provider.MetadataFeature, provider.MigrationSplitFeature, provider.DevDatabaseFeature,
		provider.DevCleanupFeature, provider.ReplayGuardFeature, provider.ObservedColumnsFeature, provider.ObservedPrimaryKeysFeature, provider.ObservedConstraintsFeature,
		provider.ObservedIndexesFeature, provider.NormalizeValuesFeature, provider.CommonObjectsFeature, provider.CommonNamespacesFeature,
		provider.LinkedReleaseLinesFeature,
	}
	c.Assert(description.GetFeatures(), qt.DeepEquals, want)
	c.Assert(description.GetEngines()[0].GetFeatures(), qt.DeepEquals, want)
	c.Assert(description.GetEngines()[0].GetCommonObjects().GetKinds(), qt.DeepEquals, []protocol.CommonObjectSupport_Kind{
		protocol.CommonObjectSupport_VIEW, protocol.CommonObjectSupport_SEQUENCE, protocol.CommonObjectSupport_COMMENT,
		protocol.CommonObjectSupport_SCHEMA,
	})
}

// The planning service owns the engine's tables through the ownership kind,
// a desired and an observed model no value of which exists, and plans them
// as table operations.
func TestDescription_DeclaresThePlanningService(t *testing.T) {
	c := testcheck.New(t)
	engine := backend.Description().GetEngines()[0]
	c.Assert(engine.GetPlanning(), qt.HasLen, 1)
	planning := engine.GetPlanning()[0]
	c.Assert(planning.GetId(), qt.Equals, "duckdb-tables")
	c.Assert(planning.GetParentKinds(), qt.DeepEquals, []string{"github.com/stokaro/ptah-duckdb-plugin/table-lifecycle"})
	c.Assert(planning.GetOperationKinds(), qt.DeepEquals, []string{"github.com/stokaro/ptah-duckdb-plugin/table-operation"})
	var models []string
	for _, model := range engine.GetValueModels() {
		models = append(models, model.GetKind()+" "+model.GetRepresentation().String())
	}
	c.Assert(models, qt.DeepEquals, []string{
		"github.com/stokaro/ptah-duckdb-plugin/table-operation OPERATION",
		"github.com/stokaro/ptah-duckdb-plugin/table-lifecycle DESIRED",
		"github.com/stokaro/ptah-duckdb-plugin/table-lifecycle OBSERVED",
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
	server.Description.Features = append(server.Description.Features, provider.SeedsFeature)
	c.Assert(server.Validate(), qt.ErrorIs, provider.ErrProtocol)
}
