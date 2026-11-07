package tableplan

import (
	"fmt"
	"slices"
	"strings"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"github.com/stokaro/ptah/provider-sdk/provider"
)

// Problem is one declared object the provider cannot create on DuckDB, with
// the reason.
type Problem struct {
	Code    protocol.SchemaDiagnostic_Code
	Kind    string
	Object  string
	Message string
}

// Validate checks every declared table offline and returns each problem in
// declaration order: what DuckDB cannot hold, and what it would hold
// differently from the declaration where no plan could converge on it.
// hold says whether DuckDB takes a table's columns, CHECKs and key as
// declared; the backend answers it with a scratch creation.
func Validate(request *protocol.ValidateSchemaRequest, hold func(*protocol.DeclaredTable) error) []Problem {
	var problems []Problem
	for _, input := range request.GetTables() {
		table := input.GetDeclaration()
		if table == nil {
			continue
		}
		name := qualifiedName(table)
		if reason := SchemaRefusal("table", table.GetSchema(), table.GetName(), request.GetTargetFeatures()); reason != "" {
			problems = append(problems, Problem{Code: protocol.SchemaDiagnostic_UNSUPPORTED_FEATURE, Kind: "table", Object: name, Message: reason})
		}
		for _, reason := range tableReasons(table, request.GetTargetFeatures()) {
			problems = append(problems, Problem{Code: protocol.SchemaDiagnostic_UNSUPPORTED_FEATURE, Kind: "table", Object: name, Message: reason})
		}
		if err := hold(table); err != nil {
			problems = append(problems, Problem{Code: protocol.SchemaDiagnostic_INVALID_SCHEMA, Kind: "table", Object: name, Message: err.Error()})
		}
	}
	return problems
}

// SchemaRefusal says why the provider cannot create an object of kind in
// schema for a host whose negotiated features are features, or returns the
// empty string. A read covers a schema other than main only for a host that
// reads schema.common-namespaces, so an object created there for any other
// host could never be read back and every later plan would fail.
func SchemaRefusal(kind, schema, name string, features []string) string {
	if slices.Contains(features, provider.CommonNamespacesFeature) || strings.TrimSpace(schema) == "" || foldASCII(schema) == DefaultSchema {
		return ""
	}
	return fmt.Sprintf("%s %q is in schema %q; the host does not read schema.common-namespaces, so the provider reads and creates objects in main only",
		kind, name, schema)
}

// qualifiedName names a declared table for a diagnostic.
func qualifiedName(table *protocol.DeclaredTable) string {
	if schema := table.GetSchema(); schema != "" && foldASCII(schema) != DefaultSchema {
		return schema + "." + table.GetName()
	}
	return table.GetName()
}

// tableReasons lists why the provider cannot create a declared table on
// DuckDB, each a sentence that names the table and what it declares.
func tableReasons(table *protocol.DeclaredTable, features []string) []string {
	name := fmt.Sprintf("table %q", qualifiedName(table))
	var reasons []string
	add := func(format string, args ...any) { reasons = append(reasons, name+" "+fmt.Sprintf(format, args...)) }
	switch {
	case strings.TrimSpace(table.GetCustomSql()) != "":
		add("declares custom SQL, which the provider does not run")
	case len(table.GetTriggers()) > 0:
		add("declares a trigger; DuckDB has no triggers")
	case strings.TrimSpace(table.GetCollation()) != "":
		add("declares a table collation; DuckDB has a collation per column only")
	case strings.TrimSpace(table.GetEngine()) != "":
		add("names a storage engine; DuckDB has none")
	}
	if len(table.GetColumns()) == 0 {
		add("declares no column")
	}
	if primaryKeySettings(table) {
		add("declares a primary key setting DuckDB does not have: deferral, INCLUDE, a method, a key order or a prefix")
	}
	for _, column := range table.GetColumns() {
		reasons = append(reasons, columnReasons(name, column)...)
	}
	for _, constraint := range table.GetConstraints() {
		reasons = append(reasons, constraintReasons(name, constraint, features)...)
	}
	for _, index := range table.GetIndexes() {
		if reason := IndexRefusal(index); reason != "" {
			add("has index %q, which %s", index.GetName(), reason)
		}
	}
	if _, err := ForeignKeys(table); err != nil {
		add("has a foreign key that %v", err)
	}
	return reasons
}

// primaryKeySettings reports whether a declared primary key states a setting
// DuckDB does not have.
func primaryKeySettings(table *protocol.DeclaredTable) bool {
	ordered := slices.ContainsFunc(table.GetPrimaryKeyParts(), func(part *protocol.DeclaredKeyPart) bool {
		return part.GetDescending() || part.GetPrefix() != ""
	})
	return ordered || table.GetPrimaryKeyDeferrable() || table.GetPrimaryKeyInitially() != "" || len(table.GetPrimaryKeyInclude()) > 0 ||
		strings.TrimSpace(table.GetPrimaryKeyMethod()) != ""
}

// columnReasons lists why the provider cannot create a declared column.
func columnReasons(table string, column *protocol.DeclaredColumn) []string {
	where := fmt.Sprintf("%s column %q", table, column.GetName())
	var reasons []string
	switch {
	case column.GetAutoIncrement() || strings.TrimSpace(column.GetIdentityGeneration()) != "":
		reasons = append(reasons, where+" is auto-increment or identity; DuckDB has neither, so declare a sequence and DEFAULT nextval('...')")
	case strings.EqualFold(strings.TrimSpace(column.GetGeneratedKind()), "STORED"):
		reasons = append(reasons, where+" is a STORED generated column; DuckDB computes generated columns when it reads a row (VIRTUAL) only")
	case column.GetCheckNotEnforced() || column.GetForeignKeyNotEnforced():
		reasons = append(reasons, where+" declares a constraint that is not enforced, which DuckDB cannot hold")
	case strings.TrimSpace(column.GetUniqueExpression()) != "":
		reasons = append(reasons, where+" declares a UNIQUE over an expression, which DuckDB cannot hold")
	}
	if strings.TrimSpace(column.GetForeignReference()) != "" {
		if reason := ruleRefusal(column.GetOnDelete(), column.GetOnUpdate(), column.GetDeferrable(), column.GetInitially(), column.GetForeignKeyMatch()); reason != "" {
			reasons = append(reasons, where+" has a foreign key that "+reason)
		}
	}
	return reasons
}

// constraintReasons lists why the provider cannot create a declared table
// constraint.
func constraintReasons(table string, constraint *protocol.DeclaredConstraint, features []string) []string {
	where := fmt.Sprintf("%s constraint %q", table, constraint.GetName())
	var reasons []string
	if constraint.GetNotEnforced() || constraint.GetNotValid() {
		reasons = append(reasons, where+" is not enforced or not valid, which DuckDB cannot hold")
	}
	switch strings.ToUpper(strings.TrimSpace(constraint.GetType())) {
	case "UNIQUE":
		switch {
		case len(constraint.GetColumns()) == 0:
			reasons = append(reasons, where+" is a UNIQUE over no column")
		case len(constraint.GetColumns()) > 1 && !slices.Contains(features, provider.ObservedUniqueConstraintsFeature):
			reasons = append(reasons, where+" is a UNIQUE over several columns, which a read reports only to a host that reads "+
				provider.ObservedUniqueConstraintsFeature+"; declare a unique index instead")
		}
		if constraint.NullsDistinct != nil || len(constraint.GetIncludeColumns()) > 0 || strings.TrimSpace(constraint.GetUsingMethod()) != "" {
			reasons = append(reasons, where+" declares NULLS DISTINCT, INCLUDE or a method, which a DuckDB UNIQUE has none of")
		}
	case "FOREIGN KEY":
		if reason := ruleRefusal(constraint.GetOnDelete(), constraint.GetOnUpdate(), constraint.GetDeferrable(), constraint.GetInitially(), constraint.GetMatch()); reason != "" {
			reasons = append(reasons, where+" "+reason)
		}
	case "CHECK", "PRIMARY KEY":
	case "EXCLUDE":
		reasons = append(reasons, where+" is an EXCLUDE constraint; DuckDB has none")
	default:
		reasons = append(reasons, fmt.Sprintf("%s has the %s constraint %q, which DuckDB does not have", table, constraint.GetType(), constraint.GetName()))
	}
	return reasons
}

// ruleRefusal says why DuckDB cannot hold a foreign key's settings: it takes
// NO ACTION and RESTRICT only, which it keeps as NO ACTION, checks a key at
// once, and has no MATCH clause.
func ruleRefusal(onDelete, onUpdate string, deferrable bool, initially, match string) string {
	for _, rule := range []struct{ name, value string }{{"ON DELETE", onDelete}, {"ON UPDATE", onUpdate}} {
		value := strings.ToUpper(strings.TrimSpace(rule.value))
		if value != "" && value != "NO ACTION" && value != "RESTRICT" {
			return fmt.Sprintf("has %s %s; DuckDB has NO ACTION and RESTRICT only", rule.name, value)
		}
	}
	switch {
	case deferrable || strings.TrimSpace(initially) != "":
		return "is deferrable; DuckDB checks a foreign key at once"
	case strings.TrimSpace(match) != "" && !strings.EqualFold(strings.TrimSpace(match), "SIMPLE"):
		return fmt.Sprintf("has MATCH %s; DuckDB has no MATCH clause", strings.ToUpper(match))
	}
	return ""
}

// IndexRefusal names what puts a declared index outside what DuckDB creates
// as declared, or returns the empty string. DuckDB's own index keeps no key
// order and no NULLS placement, so a key that states either is refused rather
// than dropped.
func IndexRefusal(declared *protocol.DeclaredIndex) string {
	switch {
	case strings.TrimSpace(declared.GetCondition()) != "":
		return "has a WHERE filter; DuckDB creates no partial index"
	case strings.TrimSpace(declared.GetType()) != "" && !strings.EqualFold(strings.TrimSpace(declared.GetType()), "ART"):
		return fmt.Sprintf("uses the method %q; the provider creates DuckDB's own ART index only", declared.GetType())
	case strings.TrimSpace(declared.GetOperator()) != "":
		return "names an operator class; a DuckDB index has none"
	case len(declared.GetStorageParameters()) > 0:
		return "sets storage parameters; a DuckDB index has none"
	case len(declared.GetIncludeColumns()) > 0:
		return "has INCLUDE columns; a DuckDB index has none"
	case declared.NullsDistinct != nil && !declared.GetNullsDistinct():
		return "is NULLS NOT DISTINCT, which a DuckDB index cannot be"
	case declared.GetInvisible():
		return "is invisible, which a DuckDB index cannot be"
	case len(declared.GetParts()) == 0 && len(declared.GetFields()) == 0:
		return "has no key"
	}
	for _, part := range declared.GetParts() {
		switch {
		case part.GetDescending() || strings.TrimSpace(part.GetNulls()) != "":
			return "states a key order or a NULLS placement; DuckDB's index keeps neither"
		case part.GetOperatorClass() != "" || part.GetPrefix() != "":
			return "has a key with an operator class or a prefix; a DuckDB index has neither"
		case part.GetColumn() == "" && strings.TrimSpace(part.GetExpression()) == "":
			return "has a key part that is neither a column nor an expression"
		}
	}
	return ""
}
