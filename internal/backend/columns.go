package backend

import (
	"context"
	"regexp"
	"strings"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	providerruntime "github.com/stokaro/ptah/provider-sdk/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/stokaro/ptah-duckdb-plugin/internal/catalog"
)

// CompareColumns judges each requested aspect of each column pair as DuckDB
// holds the declaration, which the scratch database answers: a declared TEXT
// or VARCHAR(10) and a held VARCHAR are one type, and a declared DEFAULT [1, 2]
// and a held list_value(1, 2) one default.
func (p *Provider) CompareColumns(ctx context.Context, request *protocol.CompareColumnsRequest) (*protocol.ComparedColumns, error) {
	result := &protocol.ComparedColumns{Complete: true}
	for _, table := range request.GetTables() {
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		held, err := p.hold(ctx, table.GetDesired())
		if err != nil {
			if ctx.Err() != nil {
				return nil, status.FromContextError(ctx.Err()).Err()
			}
			return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, "table "+quoted(table.GetDesired().GetName())+": "+err.Error())
		}
		output := &protocol.ComparedColumnTable{Subject: proto.CloneOf(table.GetSubject()), CurrentSubject: proto.CloneOf(table.GetCurrentSubject())}
		for _, pair := range table.GetPairs() {
			desired := findHeld(held, pair.GetDesiredColumn())
			current := findObserved(table.GetCurrent(), pair.GetCurrentColumn())
			if desired == nil || current == nil {
				return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, "a column pair names a column its table does not hold")
			}
			column := &protocol.ComparedColumn{Subject: proto.CloneOf(pair.GetSubject()), CurrentSubject: proto.CloneOf(pair.GetCurrentSubject())}
			for _, aspect := range pair.GetAspects() {
				receipt, err := compareAspect(aspect, desired, current)
				if err != nil {
					return nil, err
				}
				column.Aspects = append(column.Aspects, receipt)
			}
			output.Columns = append(output.Columns, column)
		}
		result.Tables = append(result.Tables, output)
	}
	return result, nil
}

func compareAspect(aspect protocol.ColumnAspect, desired *catalog.Column, current *protocol.ObservedColumn) (*protocol.ColumnAspectReceipt, error) {
	receipt := &protocol.ColumnAspectReceipt{Aspect: aspect}
	difference := func(same bool, current, desired string) *protocol.ColumnAspectReceipt_Difference {
		relation := protocol.ColumnDifference_CHANGED
		if same {
			relation = protocol.ColumnDifference_SAME
		}
		return &protocol.ColumnAspectReceipt_Difference{Difference: &protocol.ColumnDifference{Relation: relation, Current: current, Desired: desired}}
	}
	switch aspect {
	case protocol.ColumnAspect_COLUMN_TYPE:
		// DuckDB writes a collation as part of a column's type, and the host
		// compares no collation of its own, so the type carries it: a
		// changed collation reaches the planner, which refuses it by name.
		held, declared := withCollation(heldColumnType(current), current.GetCollation()), withCollation(desired.Type, desired.Collation)
		receipt.Receipt = difference(held == declared, held, declared)
	case protocol.ColumnAspect_COLUMN_DEFAULT:
		held, declared := text(current.DefaultExpression), text(desired.Default)
		receipt.Receipt = difference((current.DefaultExpression == nil) == (desired.Default == nil) && held == declared, held, declared)
	case protocol.ColumnAspect_COLUMN_NULLABILITY:
		receipt.Receipt = &protocol.ColumnAspectReceipt_Nullability{Nullability: &protocol.ColumnNullability{Nullable: new(desired.Nullable)}}
	case protocol.ColumnAspect_COLUMN_SEQUENCE:
		// DuckDB has no identity column, so no sequence setting differs.
		receipt.Receipt = &protocol.ColumnAspectReceipt_Sequence{Sequence: &protocol.ColumnSequenceDifferences{}}
	default:
		return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, "the provider compares the type, default, nullability and sequence of a column only")
	}
	return receipt, nil
}

// quotedDefault is a default DuckDB stores as a string literal.
var quotedDefault = regexp.MustCompile(`^'((?:[^']|'')*)'$`)

// numberDefault is a default DuckDB stores as a bare number.
var numberDefault = regexp.MustCompile(`^-?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$`)

// ReadColumnDefaults reads each stored default as a literal or an
// expression: DuckDB stores a string as 'x', a number and a boolean bare,
// and everything else, NULL included, as an expression.
func (*Provider) ReadColumnDefaults(ctx context.Context, request *protocol.ReadColumnDefaultsRequest) (*protocol.ReadColumnDefaultsResult, error) {
	result := &protocol.ReadColumnDefaultsResult{Complete: true}
	for _, input := range request.GetColumns() {
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		receipt := &protocol.ColumnDefaultReceipt{Subject: proto.CloneOf(input.GetSubject()), Kind: protocol.ColumnDefaultReceipt_ABSENT}
		if stored := input.GetColumn().DefaultExpression; stored != nil {
			value := strings.TrimSpace(*stored)
			switch match := quotedDefault.FindStringSubmatch(value); {
			case match != nil:
				receipt.Kind, receipt.Value = protocol.ColumnDefaultReceipt_LITERAL, strings.ReplaceAll(match[1], "''", "'")
			case numberDefault.MatchString(value) || value == "true" || value == "false":
				receipt.Kind, receipt.Value = protocol.ColumnDefaultReceipt_LITERAL, value
			default:
				receipt.Kind, receipt.Value = protocol.ColumnDefaultReceipt_EXPRESSION, value
			}
		}
		result.Columns = append(result.Columns, receipt)
	}
	return result, nil
}

// withCollation writes a column type with its collation, as DuckDB spells
// the two in a column definition.
func withCollation(columnType, collation string) string {
	if collation == "" {
		return columnType
	}
	return columnType + " COLLATE " + collation
}

// heldColumnType is the full type a read reports for a column.
func heldColumnType(column *protocol.ObservedColumn) string {
	if column.GetColumnType() != "" {
		return column.GetColumnType()
	}
	return column.GetDataType()
}

func findHeld(table catalog.Table, name string) *catalog.Column {
	for i := range table.Columns {
		if foldASCII(table.Columns[i].Name) == foldASCII(name) {
			return &table.Columns[i]
		}
	}
	return nil
}

func findObserved(table *protocol.ObservedTable, name string) *protocol.ObservedColumn {
	for _, column := range table.GetColumns() {
		if foldASCII(column.GetName()) == foldASCII(name) {
			return column
		}
	}
	return nil
}

// text is the value of an optional text, empty when absent.
func text(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// quoted writes a name for a reason.
func quoted(name string) string { return `"` + name + `"` }
