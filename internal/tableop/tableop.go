// Package tableop is the native operation the provider plans for a table it
// owns: one schema statement of DuckDB, kept as a structured value so the
// host can bind, digest and replay it, and written as SQL only when the host
// renders the plan.
//
// An operation names its table in its envelope's owner and carries one action
// with the operands that action needs. Normalize checks it and says what it
// does to the data; Statement writes its SQL. Both refuse an operation another
// release or another provider could have written differently, so a saved plan
// never runs something this provider would not have planned.
package tableop

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"
	"ptah.run/provider-sdk/modeljson"
	protocol "ptah.run/provider-sdk/protocol/experimental"

	"github.com/stokaro/ptah-duckdb-plugin/internal/ddl"
	"github.com/stokaro/ptah-duckdb-plugin/internal/typeloss"
)

// ProviderID is the provider that owns the operation model.
const ProviderID = "github.com/stokaro/ptah-duckdb-plugin"

// Kind is the model kind of a table operation.
const Kind = ProviderID + "/table-operation"

// Engine is the engine an operation's owner names.
const Engine = "duckdb"

const (
	schemaVersion   = 3
	encoding        = "json"
	canonicalFormat = Kind + "/json-v1"
)

// Action is what an operation does to its table.
type Action string

// The actions, one per schema statement.
const (
	CreateTable     Action = "create-table"
	DropTable       Action = "drop-table"
	AddColumn       Action = "add-column"
	DropColumn      Action = "drop-column"
	AlterColumnType Action = "alter-column-type"
	SetDefault      Action = "set-default"
	DropDefault     Action = "drop-default"
	SetNotNull      Action = "set-not-null"
	DropNotNull     Action = "drop-not-null"
	AddPrimaryKey   Action = "add-primary-key"
	AddUnique       Action = "add-unique"
	CreateIndex     Action = "create-index"
	DropIndex       Action = "drop-index"
	// DropTables drops the owner and the tables after it, one statement
	// each, in the order listed: tables a plan drops whose foreign keys
	// reference one another, which only one ordering unit can order.
	DropTables Action = "drop-tables"
)

// Operation is one action on the table its envelope names. Each action reads
// the fields its comment names and refuses any other.
type Operation struct {
	Action Action `json:"action"`
	// Columns, PrimaryKey, Uniques, Checks and ForeignKeys are the table a
	// create-table makes, the key in key order. DuckDB adds no CHECK and no
	// foreign key to a table that exists, so a create-table writes them.
	Columns     []Column     `json:"columns,omitempty"`
	PrimaryKey  []string     `json:"primaryKey,omitempty"`
	Uniques     [][]string   `json:"uniques,omitempty"`
	Checks      []string     `json:"checks,omitempty"`
	ForeignKeys []ForeignKey `json:"foreignKeys,omitempty"`
	// Column is the column an add-column adds.
	Column *Column `json:"column,omitempty"`
	// Name is the column a drop-column, alter-column-type, set-default,
	// drop-default, set-not-null or drop-not-null names, or the index a
	// drop-index drops.
	Name string `json:"name,omitempty"`
	// Type is the type an alter-column-type gives the column.
	Type string `json:"type,omitempty"`
	// Declared is the type the declaration gives the column of an
	// alter-column-type, where it states something DuckDB does not keep and
	// Type holds instead; see Column.Declared.
	Declared string `json:"declared,omitempty"`
	// Default is the SQL that follows DEFAULT in a set-default.
	Default string `json:"default,omitempty"`
	// Key is the key an add-primary-key adds, in key order, or the columns
	// of the UNIQUE an add-unique adds.
	Key []string `json:"key,omitempty"`
	// Index is the index a create-index makes.
	Index *Index `json:"index,omitempty"`
	// Tables are the tables a drop-tables drops, in order, in the owner's
	// schema; the first is the owner.
	Tables []string `json:"tables,omitempty"`
}

// Column is a column of a create-table or an add-column.
type Column struct {
	Name      string  `json:"name"`
	Type      string  `json:"type"`
	Nullable  bool    `json:"nullable"`
	Collation string  `json:"collation,omitempty"`
	Default   *string `json:"default,omitempty"`
	Generated *string `json:"generated,omitempty"`
	Unique    bool    `json:"unique,omitempty"`
	Check     string  `json:"check,omitempty"`
	// Declared is the type the declaration gives the column, where it states
	// something DuckDB does not keep, such as VARCHAR(255) for the VARCHAR
	// Type holds. It changes no statement; the render reports the omission.
	Declared string `json:"declared,omitempty"`
}

// ForeignKey is a foreign key of a create-table, into the table's schema.
type ForeignKey struct {
	Columns    []string `json:"columns"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
}

// Index is the index of a create-index: each key part is a column or an
// expression.
type Index struct {
	Name   string      `json:"name"`
	Unique bool        `json:"unique"`
	Parts  []IndexPart `json:"parts"`
}

// IndexPart is one key part of an index.
type IndexPart struct {
	Column     string `json:"column,omitempty"`
	Expression string `json:"expression,omitempty"`
}

var model = buildModel()

// Model is the operation model the provider declares.
func Model() *protocol.ValueModel { return proto.CloneOf(model) }

func buildModel() *protocol.ValueModel {
	raw, err := json.Marshal(map[string]any{
		"kind": Kind, "representation": "operation", "schema_version": schemaVersion, "encoding": encoding,
		"owner": "the table the statement changes: engine duckdb, kind table, its schema and its name; no database, catalog or path",
		"actions": []Action{CreateTable, DropTable, AddColumn, DropColumn, AlterColumnType, SetDefault, DropDefault, SetNotNull, DropNotNull,
			AddPrimaryKey, AddUnique, CreateIndex, DropIndex, DropTables},
		"fields": map[string]string{
			"action": "required; one schema statement of DuckDB",
			"columns": "create-table: every column in order, each with name, type as DuckDB reads it, nullable, and an optional collation, " +
				"default, VIRTUAL generated expression, UNIQUE and CHECK",
			"primaryKey":  "create-table: the key columns in key order",
			"uniques":     "create-table: each UNIQUE constraint over several columns, its columns in order; one over a single column is its column's unique",
			"checks":      "create-table: the table's CHECK conditions",
			"foreignKeys": "create-table: each foreign key's columns, the referenced table in the same schema and its columns",
			"column":      "add-column: one column without UNIQUE, CHECK or generated expression",
			"name":        "the column an alter, drop-column or drop-default names, or the index a drop-index drops",
			"type":        "alter-column-type: the type as DuckDB reads it",
			"declared": "alter-column-type, and a column of create-table or add-column: the declared type where it states a length or a precision " +
				"DuckDB does not keep, which the render reports as left out",
			"default": "set-default: the SQL that follows DEFAULT",
			"key":     "add-primary-key and add-unique: the columns in order",
			"index":   "create-index: name, unique and key parts, each a column or an expression",
			"tables":  "drop-tables: two or more distinct tables of the owner's schema in the order they are dropped, the owner first",
		},
		"execution": "one statement; DuckDB runs every one inside a transaction",
	})
	if err != nil {
		panic(err)
	}
	canonical, err := modeljson.Canonical(raw)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(canonical)
	return &protocol.ValueModel{Kind: Kind, Representation: protocol.ValueInput_OPERATION, SchemaVersion: schemaVersion, Encoding: encoding,
		DefinitionJson: canonical, DefinitionSha256: hex.EncodeToString(sum[:]), CanonicalFormat: canonicalFormat}
}

// Owner is the reference an operation on the table schema.name names.
func Owner(schema, name string) *protocol.Reference {
	return &protocol.Reference{Engine: Engine, Kind: "table", Schema: schema, Name: name}
}

// Capture builds the normalized operation id that applies operation to the
// table owner names.
func Capture(id string, owner *protocol.Reference, operation Operation) (*protocol.NormalizedValue, error) {
	payload, err := json.Marshal(operation)
	if err != nil {
		return nil, err
	}
	return Normalize(&protocol.ValueInput{Id: id, Representation: protocol.ValueInput_OPERATION, Value: &protocol.Envelope{
		ProviderId: ProviderID, Kind: Kind, Owner: proto.CloneOf(owner), SchemaVersion: schemaVersion, Encoding: encoding,
		DefinitionSha256: model.GetDefinitionSha256(), Payload: payload,
	}})
}

// Normalize checks an operation value and returns it in canonical form, with
// what it does to the data. It refuses an envelope of another model, an owner
// that is not a table, and an action whose operands are missing, extra or
// unwritable.
func Normalize(input *protocol.ValueInput) (*protocol.NormalizedValue, error) {
	value := input.GetValue()
	if input.GetRepresentation() != protocol.ValueInput_OPERATION || value.GetProviderId() != ProviderID || value.GetKind() != Kind ||
		value.GetSchemaVersion() != schemaVersion || value.GetEncoding() != encoding || value.GetDefinitionSha256() != model.GetDefinitionSha256() {
		return nil, fmt.Errorf("a DuckDB table operation does not match its model")
	}
	owner := value.GetOwner()
	if owner.GetEngine() != Engine || owner.GetKind() != "table" || owner.GetName() == "" || owner.GetSchema() == "" ||
		owner.GetDatabase() != "" || owner.GetCatalog() != "" || len(owner.GetPath()) != 0 || len(owner.GetOverload()) != 0 {
		return nil, fmt.Errorf("a DuckDB table operation names a table by its schema and name and nothing else")
	}
	operation, err := Decode(value.GetPayload())
	if err != nil {
		return nil, err
	}
	statement, err := operation.statement(owner.GetSchema(), owner.GetName())
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(operation)
	if err != nil {
		return nil, err
	}
	canonical, err := modeljson.Canonical(payload)
	if err != nil {
		return nil, err
	}
	normalized := proto.CloneOf(value)
	normalized.Payload = canonical
	impact, reason := operation.impact()
	return &protocol.NormalizedValue{Id: input.GetId(), Value: normalized, NormalizedOwner: proto.CloneOf(owner),
		CanonicalFormat: canonicalFormat, CanonicalProjection: []byte(statement),
		OperationSemantics: &protocol.OperationSemantics{Impact: impact, Reason: reason,
			Access: protocol.ChangeSemantics_UNCHANGED, AccessReason: "A table statement grants and revokes nothing"}}, nil
}

// Decode reads an operation payload, refusing a field the model does not
// have.
func Decode(payload []byte) (Operation, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var operation Operation
	if err := decoder.Decode(&operation); err != nil {
		return Operation{}, fmt.Errorf("a DuckDB table operation is not one the provider writes: %w", err)
	}
	if decoder.More() {
		return Operation{}, fmt.Errorf("a DuckDB table operation carries data after its value")
	}
	return operation, nil
}

// Statement writes the SQL of a normalized operation value. It normalizes the
// value again and refuses one whose stored form differs, so a value changed
// after planning never runs.
func Statement(captured *protocol.NormalizedValue) (string, error) {
	normalized, err := Normalize(&protocol.ValueInput{Id: captured.GetId(), Representation: protocol.ValueInput_OPERATION, Value: captured.GetValue()})
	if err != nil {
		return "", err
	}
	if !proto.Equal(normalized, captured) {
		return "", fmt.Errorf("a DuckDB table operation changed after it was planned")
	}
	return string(normalized.GetCanonicalProjection()), nil
}

// SQL writes the statement of o for the table schema.table, checking its
// operands as Normalize does.
func (o Operation) SQL(schema, table string) (string, error) { return o.statement(schema, table) }

// fields lists the operands an action reads.
var fields = map[Action][]string{
	CreateTable:     {"columns", "primaryKey", "uniques", "checks", "foreignKeys"},
	DropTable:       {},
	AddColumn:       {"column"},
	DropColumn:      {"name"},
	AlterColumnType: {"name", "type", "declared"},
	SetDefault:      {"name", "default"},
	DropDefault:     {"name"},
	SetNotNull:      {"name"},
	DropNotNull:     {"name"},
	AddPrimaryKey:   {"key"},
	AddUnique:       {"key"},
	CreateIndex:     {"index"},
	DropIndex:       {"name"},
	DropTables:      {"tables"},
}

// present lists the operands an operation sets.
func (o Operation) present() []string {
	var names []string
	for name, set := range map[string]bool{
		"columns": len(o.Columns) != 0, "primaryKey": len(o.PrimaryKey) != 0, "uniques": len(o.Uniques) != 0, "checks": len(o.Checks) != 0,
		"foreignKeys": len(o.ForeignKeys) != 0, "column": o.Column != nil, "name": o.Name != "", "type": o.Type != "", "declared": o.Declared != "",
		"default": o.Default != "",
		"key":     len(o.Key) != 0, "index": o.Index != nil, "tables": len(o.Tables) != 0,
	} {
		if set {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// statement checks the operands of o and writes its SQL for the table
// schema.table.
func (o Operation) statement(schema, table string) (string, error) {
	allowed, known := fields[o.Action]
	if !known {
		return "", fmt.Errorf("a DuckDB table operation has the unknown action %q", o.Action)
	}
	for _, name := range o.present() {
		if !slices.Contains(allowed, name) {
			return "", fmt.Errorf("a %s operation does not take %s", o.Action, name)
		}
	}
	for _, declared := range o.DeclaredTypes() {
		if _, lost := typeloss.Of(declared.Type); !lost {
			return "", fmt.Errorf("the %s operation declares column %q as %s, which DuckDB keeps whole", o.Action, declared.Column, declared.Type)
		}
	}
	switch o.Action {
	case CreateTable:
		definition := ddl.Table{PrimaryKey: o.PrimaryKey, Uniques: o.Uniques, Checks: o.Checks}
		for _, column := range o.Columns {
			definition.Columns = append(definition.Columns, column.ddl())
		}
		for _, key := range o.ForeignKeys {
			definition.ForeignKeys = append(definition.ForeignKeys, ddl.ForeignKey{Columns: key.Columns, RefTable: key.RefTable, RefColumns: key.RefColumns})
		}
		return ddl.CreateTable(schema, table, definition)
	case DropTable:
		return ddl.DropTable(schema, table), nil
	case DropTables:
		return dropTables(schema, table, o.Tables)
	case AddColumn:
		if o.Column == nil {
			return "", fmt.Errorf("an add-column operation names no column")
		}
		return ddl.AddColumn(schema, table, o.Column.ddl())
	case AddPrimaryKey:
		return ddl.AddPrimaryKey(schema, table, o.Key)
	case AddUnique:
		return ddl.AddUnique(schema, table, o.Key)
	case CreateIndex:
		if o.Index == nil {
			return "", fmt.Errorf("a create-index operation names no index")
		}
		index := ddl.Index{Name: o.Index.Name, Unique: o.Index.Unique}
		for _, part := range o.Index.Parts {
			index.Parts = append(index.Parts, ddl.IndexPart{Column: part.Column, Expression: part.Expression})
		}
		return ddl.CreateIndex(schema, table, index)
	}
	return o.namedStatement(schema, table)
}

// DeclaredType is the type the declaration gives a column, where it states
// something DuckDB does not keep.
type DeclaredType struct {
	Column, Type string
}

// DeclaredTypes lists the declared types an operation carries beside the
// types it writes, in column order: those of a create-table's columns, an
// add-column's column and an alter-column-type's column.
func (o Operation) DeclaredTypes() []DeclaredType {
	var declared []DeclaredType
	columns := o.Columns
	if o.Column != nil {
		columns = append(slices.Clone(columns), *o.Column)
	}
	for _, column := range columns {
		if column.Declared != "" {
			declared = append(declared, DeclaredType{Column: column.Name, Type: column.Declared})
		}
	}
	if o.Declared != "" {
		declared = append(declared, DeclaredType{Column: o.Name, Type: o.Declared})
	}
	return declared
}

// namedStatement writes the SQL of an action that names one column or index
// of the table schema.table.
func (o Operation) namedStatement(schema, table string) (string, error) {
	if o.Name == "" {
		return "", fmt.Errorf("a %s operation names nothing", o.Action)
	}
	switch o.Action {
	case DropColumn:
		return ddl.DropColumn(schema, table, o.Name), nil
	case AlterColumnType:
		return ddl.AlterColumnType(schema, table, o.Name, o.Type)
	case SetDefault:
		return ddl.SetDefault(schema, table, o.Name, o.Default)
	case DropDefault:
		return ddl.DropDefault(schema, table, o.Name), nil
	case SetNotNull:
		return ddl.SetNotNull(schema, table, o.Name), nil
	case DropNotNull:
		return ddl.DropNotNull(schema, table, o.Name), nil
	default:
		return ddl.DropIndex(schema, o.Name), nil
	}
}

// impact says what o does to the rows of its table, and why.
func (o Operation) impact() (protocol.ChangeSemantics_Impact, string) {
	switch o.Action {
	case DropTable:
		return protocol.ChangeSemantics_DESTRUCTIVE, "Drops a table and its rows"
	case DropTables:
		return protocol.ChangeSemantics_DESTRUCTIVE, "Drops tables and their rows, each after the tables whose foreign keys reference it"
	case DropColumn:
		return protocol.ChangeSemantics_DESTRUCTIVE, "Drops a column and its values"
	case AlterColumnType:
		return protocol.ChangeSemantics_BEHAVIORAL, "Changes the type of a column; DuckDB casts every value and refuses a value the new type cannot hold"
	case SetNotNull:
		return protocol.ChangeSemantics_BEHAVIORAL, "Makes a column NOT NULL; DuckDB refuses it while a row holds NULL there"
	case AddPrimaryKey:
		return protocol.ChangeSemantics_BEHAVIORAL, "Adds a primary key; DuckDB refuses it while two rows share a key or a key holds NULL"
	case AddUnique:
		return protocol.ChangeSemantics_BEHAVIORAL, "Adds a UNIQUE constraint; DuckDB refuses it while two rows share its values"
	case CreateIndex:
		if o.Index != nil && o.Index.Unique {
			return protocol.ChangeSemantics_BEHAVIORAL, "Creates a unique index; DuckDB refuses it while two rows share a key"
		}
		return protocol.ChangeSemantics_ADDITIVE, "Creates an index"
	case DropIndex:
		return protocol.ChangeSemantics_ADDITIVE, "Drops an index; no row changes"
	case CreateTable:
		return protocol.ChangeSemantics_ADDITIVE, "Creates a table"
	case AddColumn:
		if o.Column != nil && !o.Column.Nullable && o.Column.Default == nil {
			return protocol.ChangeSemantics_BEHAVIORAL, "Adds a NOT NULL column without a default; DuckDB refuses it while the table holds a row"
		}
		return protocol.ChangeSemantics_ADDITIVE, "Adds a column"
	default:
		return protocol.ChangeSemantics_ADDITIVE, "Changes a column's default or makes it nullable; no row changes"
	}
}

func (c Column) ddl() ddl.Column {
	return ddl.Column{Name: c.Name, Type: c.Type, Nullable: c.Nullable, Collation: c.Collation, Default: c.Default, Generated: c.Generated,
		Unique: c.Unique, Check: c.Check}
}

// dropTables writes one DROP TABLE per table, in order, separated by
// semicolons: a unit DuckDB runs statement by statement. owner must be the
// first table and the tables distinct.
func dropTables(schema, owner string, tables []string) (string, error) {
	if len(tables) < 2 || tables[0] != owner {
		return "", fmt.Errorf("a drop-tables operation drops two or more tables, its owner first")
	}
	statements := make([]string, len(tables))
	for i, table := range tables {
		if table == "" || slices.Contains(tables[:i], table) {
			return "", fmt.Errorf("a drop-tables operation names each table once")
		}
		statements[i] = ddl.DropTable(schema, table)
	}
	return strings.Join(statements, ";\n"), nil
}
