// Package backend serves DuckDB to Ptah through the experimental provider
// protocol. It declares the provider and its engine and serves sessions: Open
// reads a duckdb:// address (see the address package), opens the database
// file or in-memory database it names through the official driver with the
// provider's extension and file access policy (see the instances package),
// and reports the embedded DuckDB's version, the session's identifier rules
// and the capabilities measured on that DuckDB.
//
// Inspect reads the schemas of a database from DuckDB's catalog (see the
// catalog package): the schemas themselves, tables with their columns in
// DuckDB's own type spelling, primary keys, UNIQUE, CHECK and FOREIGN KEY
// constraints and indexes, and views, sequences and comments. A read
// describes its scope completely or fails with a reason that names what it
// does not describe.
//
// The provider's planning service owns the engine's tables
// (planning.table-ownership), and an enum column is DuckDB's ENUM type that
// lists its values (schema.enum-columns): ValidateSchema
// checks a declaration offline, the table services say how DuckDB holds a
// declared table (asking a private in-memory database, see the scratch
// package), and the planning service plans every create, change and drop as
// DuckDB statements or refuses it by name (see the tableplan package). Render
// writes a plan's statements, Execute runs them in the host's transaction,
// and AnalyzeSQL splits migration files as DuckDB reads them (see the sqltext
// package).
//
// The provider keeps the records of Ptah's migrator, its revisions, run log
// and tags, in tables it marks as its own (see the history, metadata and
// ownedtable packages), splits a migration body into its statements (see
// migrationunits), and provisions, cleans and guards dev databases (see
// devclean).
package backend

import (
	"context"
	"sync"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"github.com/stokaro/ptah/provider-sdk/provider"

	"github.com/stokaro/ptah-duckdb-plugin/internal/buildversion"
	"github.com/stokaro/ptah-duckdb-plugin/internal/instances"
	"github.com/stokaro/ptah-duckdb-plugin/internal/scratch"
	"github.com/stokaro/ptah-duckdb-plugin/internal/tableop"
	"github.com/stokaro/ptah-duckdb-plugin/internal/tableplan"
)

// ProviderID is the identity of this provider and the namespace of its model
// kinds.
const ProviderID = "github.com/stokaro/ptah-duckdb-plugin"

// engineID is the engine name the host selects this provider by. It is also
// the scheme of the addresses Open accepts.
const engineID = "duckdb"

// defaultSchema is the schema DuckDB creates in every database and resolves
// an unqualified name in.
const defaultSchema = "main"

// semanticIdentity names what the provider's readings and statements mean. A
// saved plan binds to it, so it changes when the same input would mean
// something else.
const semanticIdentity = "duckdb-1"

// minimumContract is the oldest contract a host must speak to start the
// provider: 25.32, whose table ownership (planning.table-ownership) is the
// provider's only claim on the engine's tables. A host of an earlier
// contract would hand the provider no table to plan. 25.32 also states the
// working directory a relative duckdb:// path resolves against (25.30). The
// SDK the provider is built with speaks a later contract, whose additions
// the provider uses only when the host lists them.
const minimumContract = 32

// Provider serves the duckdb engine. The zero value is ready to use, and one
// value serves every session of the process.
type Provider struct {
	mu        sync.Mutex
	sessions  map[string]*session
	instances instances.Registry
	// scratch answers how DuckDB holds what a declaration writes; see the
	// scratch package.
	scratch scratch.Engine
	// locks are the locks the process's sessions hold, by database and
	// name; see locks.go. lockMu guards them.
	lockMu sync.Mutex
	locks  map[lockKey]heldLock
	// devDirs are the directories of the dev databases the provider
	// created, by lease; see devdb.go. devMu guards them.
	devMu   sync.Mutex
	devDirs map[string]string
}

// session is one open session: a connection of its own to a database the
// registry holds. Its mutex serializes the calls that use the connection,
// because DuckDB runs one statement at a time on a connection.
type session struct {
	mu     sync.Mutex
	handle *instances.Handle
	// database is the catalog name DuckDB gives the database: the file's
	// name without its extension, or memory.
	database string
	// lockIDs are the session's lock IDs and the outcome of each; see
	// locks.go. Provider.lockMu guards them.
	lockIDs map[string]*lockRecord
	// transactions are the session's transaction handles by ID, and active
	// the one whose transaction is open; see transactions.go. mu guards them.
	transactions map[string]*transaction
	active       string
	// readers are the session's metadata snapshots by reader ID; see
	// metadata.go. mu guards them.
	readers map[string]*reader
}

// New returns a provider with no open session.
func New() *Provider { return &Provider{} }

// providerFeatures are the services and behaviors the provider offers.
func providerFeatures() []string {
	return []string{
		provider.InspectFeature,
		provider.RenderFeature,
		provider.ExecuteFeature,
		provider.AnalyzeSQLFeature,
		// A failed call carries a reason the provider wrote.
		provider.ErrorReasonsFeature,
		// The provider owns the lifecycle of the engine's tables: it checks a
		// declaration, states its key columns, predicts what a creation holds,
		// judges columns in DuckDB's spelling, and plans every statement
		// through its planning service; see tableplan.
		provider.ValidateSchemaFeature,
		provider.PrepareTablesFeature,
		provider.ProjectTableCreationsFeature,
		provider.ColumnsFeature,
		provider.PlanFeaturesFeature,
		provider.PlanningLifecycleFeature,
		// The planning service owns every table of the engine without a
		// table setting model.
		provider.TableOwnershipFeature,
		// A changed key and a changed CHECK, foreign key or UNIQUE reach the
		// planning service, which plans or refuses them by name.
		provider.PrimaryKeyChangesFeature,
		provider.ConstraintChangesFeature,
		provider.CheckEquivalenceFeature,
		// Every Ptah command plans in the exact statement style, the only one
		// the provider writes.
		provider.PlanningPolicyFeature,
		provider.PlanningStatementStyleFeature,
		// The host takes a lock before it applies a plan, and runs a plan in
		// a transaction; see locks.go and transactions.go.
		provider.LocksFeature,
		provider.TransactionsFeature,
		// Ptah's migrator keeps its revision records, its run log and its
		// tags in tables the provider marks as its own; see history.go and
		// metadata.go.
		provider.HistoryFeature,
		provider.MetadataFeature,
		// The provider splits a migration body into its statements, each of
		// which runs in a transaction with its checkpoint, so a migration
		// runs without a transaction of its own too; see split.go.
		provider.MigrationSplitFeature,
		// `--dev-url docker://duckdb` provisions a database file in a
		// temporary directory, and the host cleans a dev database and
		// guards a replay into it; see devdb.go and devclean.go.
		provider.DevDatabaseFeature,
		provider.DevCleanupFeature,
		provider.ReplayGuardFeature,
		// A read reports each column whole, the primary key in key order,
		// the CHECKs, foreign keys and UNIQUE constraints, and the indexes a
		// table holds; a column comparison states collation apart from the
		// type; and an enum column reaches the provider as an ENUM type.
		provider.ObservedColumnsFeature,
		provider.ObservedPrimaryKeysFeature,
		provider.ObservedConstraintsFeature,
		provider.ObservedUniqueConstraintsFeature,
		provider.ObservedIndexesFeature,
		provider.ColumnCollationsFeature,
		provider.EnumColumnsFeature,
		// The provider normalizes its table operations.
		provider.NormalizeValuesFeature,
		// Views, sequences, comments and schemas travel on the common-object
		// wire, and the provider decides whether a declared view's query and
		// the one DuckDB holds are one query; see CompareViewQueries.
		provider.CommonObjectsFeature,
		provider.CommonNamespacesFeature,
		provider.ViewQueryEquivalenceFeature,
		// A render names each column whose declared type states what DuckDB
		// does not keep, such as a VARCHAR length; see typeloss.
		provider.RenderOmissionsFeature,
		// The release line names the DuckDB the executable links as its
		// evidence; see identity.go.
		provider.LinkedReleaseLinesFeature,
	}
}

// Description declares the provider and the duckdb engine.
func Description() *protocol.Description {
	return &protocol.Description{
		Revision:         provider.Revision,
		MinContract:      &protocol.ContractVersion{Major: provider.Revision, Minor: minimumContract},
		ProviderId:       ProviderID,
		Version:          buildversion.Version(),
		SemanticIdentity: semanticIdentity,
		Features:         providerFeatures(),
		Engines: []*protocol.Engine{{
			Id:            engineID,
			Aliases:       aliases(),
			DefaultSchema: defaultSchema,
			Identifiers:   identifierRules(),
			ReleaseLines:  releaseLines(),
			Features:      providerFeatures(),
			CommonObjects: &protocol.CommonObjectSupport{Kinds: commonObjectKinds()},
			ValueModels:   []*protocol.ValueModel{tableop.Model()},
			// DuckDB's type lists an enum column's values: ENUM('a', 'b').
			EnumColumns: &protocol.EnumColumns{Form: protocol.EnumColumns_VALUES_TYPE},
			Planning: []*protocol.ModelPlanning{{
				Id: tableplan.ServiceID, OwnsTables: true, OperationKinds: []string{tableop.Kind},
			}},
		}},
	}
}

// negotiatedFeatures is the engine's feature list as the host holds it: the
// two-way features the host did not list are left out, as the SDK leaves them
// out of the description the host reads. A session reports it, so the host
// can check it against that description.
func negotiatedFeatures(ctx context.Context) []string {
	return provider.NegotiatedDescription(Description(), provider.HostFeatures(ctx)).GetEngines()[0].GetFeatures()
}
