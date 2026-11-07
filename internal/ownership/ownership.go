// Package ownership declares the table setting the provider's planning
// service is assigned by. The host assigns a table to the planning service
// that owns one of the table's settings, and DuckDB's tables have no setting
// of their own, so the provider declares this model to own their lifecycle.
// No value of it exists: no source declares one, no read reports one, and
// NormalizeValues refuses one.
package ownership

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/stokaro/ptah/provider-sdk/modeljson"
	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"google.golang.org/protobuf/proto"

	"github.com/stokaro/ptah-duckdb-plugin/internal/tableplan"
)

// Kind is the model kind of the setting.
const Kind = tableplan.OwnershipKind

const (
	schemaVersion = 1
	encoding      = "json"
)

var models = buildModels()

// Models are the desired and observed models of the setting.
func Models() []*protocol.ValueModel {
	return []*protocol.ValueModel{proto.CloneOf(models[0]), proto.CloneOf(models[1])}
}

func buildModels() [2]*protocol.ValueModel {
	var result [2]*protocol.ValueModel
	for i, representation := range []protocol.ValueInput_Representation{protocol.ValueInput_DESIRED, protocol.ValueInput_OBSERVED} {
		raw, err := json.Marshal(map[string]any{
			"kind": Kind, "representation": strings.ToLower(representation.String()), "schema_version": schemaVersion, "encoding": encoding,
			"owner":   "the table: engine duckdb, kind table, its schema and its name",
			"purpose": "assigns the engine's tables to the provider's planning service; no value of it exists",
			"fields":  make(map[string]string),
		})
		if err != nil {
			panic(err)
		}
		canonical, err := modeljson.Canonical(raw)
		if err != nil {
			panic(err)
		}
		sum := sha256.Sum256(canonical)
		result[i] = &protocol.ValueModel{Kind: Kind, Representation: representation, SchemaVersion: schemaVersion, Encoding: encoding,
			DefinitionJson: canonical, DefinitionSha256: hex.EncodeToString(sum[:]), CanonicalFormat: Kind + "/json-v1"}
	}
	return result
}
