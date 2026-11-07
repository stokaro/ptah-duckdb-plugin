// Command ptah-plugin-duckdb serves DuckDB through the experimental Ptah
// protocol. It embeds DuckDB through the official Go driver, which links the
// DuckDB library with cgo, and serves sessions on database files and
// in-memory databases, and reads their schemas.
package main

import (
	"fmt"
	"os"

	"github.com/stokaro/ptah/provider-sdk/runtime"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
)

func main() {
	if err := runtime.Serve(backend.Server()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
