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
// The provider plans, renders and runs nothing yet.
package backend

import (
	"context"
	"sync"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"github.com/stokaro/ptah/provider-sdk/provider"

	"github.com/stokaro/ptah-duckdb-plugin/internal/buildversion"
	"github.com/stokaro/ptah-duckdb-plugin/internal/instances"
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
// provider: the one the SDK it is built with speaks. An older host is refused
// before a session opens.
const minimumContract = 29

// Provider serves the duckdb engine. The zero value is ready to use, and one
// value serves every session of the process.
type Provider struct {
	mu        sync.Mutex
	sessions  map[string]*session
	instances instances.Registry
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
}

// New returns a provider with no open session.
func New() *Provider { return &Provider{} }

// providerFeatures are the services and behaviors the provider offers.
func providerFeatures() []string {
	return []string{
		provider.InspectFeature,
		// A failed call carries a reason the provider wrote.
		provider.ErrorReasonsFeature,
		// A read reports each column whole, the primary key in key order,
		// the CHECKs and foreign keys, and the indexes a table holds.
		provider.ObservedColumnsFeature,
		provider.ObservedPrimaryKeysFeature,
		provider.ObservedConstraintsFeature,
		provider.ObservedIndexesFeature,
		// The SDK pairs the observations above with the normalization
		// service; the provider declares no model for it.
		provider.NormalizeValuesFeature,
		// Views, sequences, comments and schemas travel on the common-object
		// wire.
		provider.CommonObjectsFeature,
		provider.CommonNamespacesFeature,
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
