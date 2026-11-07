// Command ptah-plugin-duckdb serves DuckDB through the experimental Ptah
// protocol. It embeds DuckDB through the official Go driver, which links the
// DuckDB library with cgo, and serves sessions on database files and
// in-memory databases. It serves no schema service yet.
package main

import (
	"fmt"
	"os"

	"github.com/stokaro/ptah/provider-sdk/runtime"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
)

func main() {
	if err := runtime.Serve(runtime.Server{Description: backend.Description(), Sessions: backend.New()}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
