package backend

import "github.com/stokaro/ptah/provider-sdk/runtime"

// Server joins the description with the services that implement it. Each
// service the description offers has an implementation, and runtime.Serve
// refuses a server where the two differ.
func Server() runtime.Server {
	duckdb := New()
	return runtime.Server{
		Description:     Description(),
		Sessions:        duckdb,
		Inspector:       duckdb,
		Renderer:        duckdb,
		Executor:        duckdb,
		SQLAnalyzer:     duckdb,
		ValueNormalizer: duckdb,
		CommonObjects:   duckdb,
		SchemaValidator: duckdb,
		TablePreparer:   duckdb,
		TableCreations:  duckdb,
		Columns:         duckdb,
		FeaturePlanner:  duckdb,
		Locks:           duckdb,
		Transactions:    duckdb,
		History:         duckdb,
		Metadata:        duckdb,
		MigrationSplits: duckdb,
		DevProvisioner:  duckdb,
		DevCleaner:      duckdb,
		ReplayGuard:     duckdb,
		CheckComparer:   duckdb,
	}
}
