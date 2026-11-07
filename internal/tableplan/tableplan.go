// Package tableplan plans the tables of the duckdb engine: their creation,
// the changes of a table the plan keeps, and their removal. It is the
// planning service that owns the engine's tables (planning.table-ownership),
// which the host hands every table it creates, changes or drops, with the
// host's placeholder steps for each.
//
// Each statement is a step of its own with a tableop operation. A step
// replaces the host placeholder it stands for, or joins the plan beside them,
// ordered by explicit dependencies: a table after the tables its foreign keys
// reference, an index after its table, a column change after the index
// removals of its table and before its index additions, and a key or a
// UNIQUE after the steps that add or change its columns. Tables the plan
// drops whose foreign keys reference one another are dropped by one step,
// each after the tables that reference it.
//
// DuckDB refuses many changes of a table that exists, and the planner refuses
// each one by name before a statement is sent, never planning it as a
// removal and a new creation. Measured on the embedded DuckDB:
//
//   - while a table has an index, DuckDB changes no column's type or
//     nullability, makes no column UNIQUE, adds no NOT NULL or UNIQUE column
//     and drops no column ("Cannot alter entry ... because there are entries
//     that depend on it"); the same holds while a foreign key of another
//     table references the table. A default changes and a plain column is
//     added either way;
//   - an index dropped in the plan's transaction stays until the commit, so
//     in that transaction DuckDB still changes the type of no column the
//     index covers and drops no column the index covers nor one before such
//     a column;
//   - it changes the type of no column in a primary key, a UNIQUE, a CHECK or
//     a foreign key, and drops no column in a primary key, a UNIQUE, a
//     foreign key or an index, nor one that a primary key or UNIQUE column
//     follows;
//   - it adds and drops no CHECK and no foreign key on a table that exists,
//     drops no UNIQUE, and changes and drops no primary key.
//
// DuckDB keeps no constraint names, so the planner compares a kept table's
// CHECKs and foreign keys by what they say and its UNIQUEs by their columns
// in order, never by name.
package tableplan

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"github.com/stokaro/ptah/provider-sdk/provider"
	"google.golang.org/protobuf/proto"

	"github.com/stokaro/ptah-duckdb-plugin/internal/tableop"
)

// ServiceID names the table lifecycle service.
const ServiceID = "duckdb-tables"

// The ranks order steps that nothing else orders: the host schedules ready
// steps by owner and then by name, so a table comes before its indexes, and
// an index removal before a column change.
const (
	rankTable       = "1-table"
	rankIndexDrop   = "2-index"
	rankColumn      = "3-column"
	rankIndexCreate = "4-index"
)

// planner is the state of one planning request.
type planner struct {
	request      *protocol.PlanFeaturesRequest
	declared     Declared
	contribution *protocol.PlanningContribution
	result       *protocol.PlannedFeatures
	// receipts are the steps each table's parent receipt names, by the
	// table's index in the request.
	receipts map[int][]*protocol.StepIdentity
	// created and dropped are the steps that create and drop each table the
	// plan creates and drops.
	created map[tableKey]*protocol.StepIdentity
	dropped map[tableKey]*protocol.StepIdentity
	// tables are the tables of the request by key.
	tables map[tableKey]*protocol.PlanningTable
}

// tableKey names a table by its schema and its name, folded as DuckDB
// compares them.
type tableKey struct{ schema, name string }

// refusal is a change the service cannot plan, at the table index of the
// request it belongs to.
type refusal struct {
	table   int
	code    protocol.PlanningDiagnostic_Code
	message string
}

func (r *refusal) Error() string { return r.message }

func unsupported(index int, format string, args ...any) error {
	return &refusal{table: index, code: protocol.PlanningDiagnostic_UNSUPPORTED_FEATURE, message: fmt.Sprintf(format, args...)}
}

// Plan plans every table the request creates, changes or drops, and returns
// the steps, the rewrites that replace the host's placeholders, and a receipt
// for each table the service owns. declared says how DuckDB holds each
// declared table. A change the service cannot plan returns a completed
// refusal naming its table.
func Plan(ctx context.Context, request *protocol.PlanFeaturesRequest, declared Declared) (*protocol.PlannedFeatures, error) {
	if request.GetPlanningId() != ServiceID {
		return nil, fmt.Errorf("the request names another planning service")
	}
	p := &planner{
		request:      request,
		declared:     declared,
		contribution: &protocol.PlanningContribution{Owner: tableop.ProviderID},
		result:       &protocol.PlannedFeatures{Complete: true},
		receipts:     make(map[int][]*protocol.StepIdentity),
		created:      make(map[tableKey]*protocol.StepIdentity),
		dropped:      make(map[tableKey]*protocol.StepIdentity),
		tables:       make(map[tableKey]*protocol.PlanningTable),
	}
	for _, table := range request.GetTables() {
		p.tables[keyOf(table.GetSubject())] = table
	}
	for _, action := range []protocol.PlanningTable_Action{protocol.PlanningTable_CREATE, protocol.PlanningTable_DROP, protocol.PlanningTable_ALTER,
		protocol.PlanningTable_REBUILD} {
		for i, table := range request.GetTables() {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if table.GetAction() != action || action == protocol.PlanningTable_DROP {
				continue
			}
			if err := p.plan(i, table); err != nil {
				return refused(err)
			}
		}
		if action == protocol.PlanningTable_DROP {
			if err := p.planDrops(); err != nil {
				return refused(err)
			}
		}
	}
	if err := p.order(); err != nil {
		return refused(err)
	}
	if err := p.claimRest(); err != nil {
		return refused(err)
	}
	for i, table := range request.GetTables() {
		if table.GetAction() == protocol.PlanningTable_CONTEXT {
			continue
		}
		// The service owns the engine's tables (planning.table-ownership),
		// so the table receipt follows the receipts of any parent kind.
		for _, kind := range append(slices.Clone(request.GetParentKinds()), provider.TableReceiptKind) {
			p.result.Parents = append(p.result.Parents, &protocol.PlanningParentReceipt{Action: table.GetAction(), Receipt: &protocol.PlanningReceipt{
				Subject: proto.CloneOf(table.GetSubject()), Kind: kind, Strategy: strategy(table.GetAction()), Steps: cloneSteps(p.receipts[i]),
			}})
		}
	}
	if len(p.contribution.GetSteps()) > 0 {
		p.result.Contributions = []*protocol.PlanningContribution{p.contribution}
	}
	return p.result, ctx.Err()
}

func refused(err error) (*protocol.PlannedFeatures, error) {
	declined, ok := errors.AsType[*refusal](err)
	if !ok {
		return nil, err
	}
	return &protocol.PlannedFeatures{Complete: true, Diagnostics: []*protocol.PlanningDiagnostic{{
		Code: declined.code, Kind: provider.TableReceiptKind, Message: declined.message,
		Input: &protocol.PlanningDiagnostic_Parent{Parent: uint32(declined.table)}, // #nosec G115 -- a request holds far fewer tables than math.MaxUint32
	}}}, nil
}

func strategy(action protocol.PlanningTable_Action) string {
	switch action {
	case protocol.PlanningTable_CREATE:
		return "Create the table with its constraints, then its indexes"
	case protocol.PlanningTable_DROP:
		return "Drop the table after the tables whose foreign keys reference it"
	default:
		return "Change the table one statement at a time"
	}
}

func (p *planner) plan(index int, table *protocol.PlanningTable) error {
	switch table.GetAction() {
	case protocol.PlanningTable_CREATE:
		return p.create(index, table)
	case protocol.PlanningTable_ALTER:
		return p.alter(index, table)
	default:
		return unsupported(index, "table %s: the provider plans no rebuild of a table", phrase(table.GetSubject()))
	}
}

// create replaces the host's creation of a table with CREATE TABLE, which
// holds the table's CHECKs and foreign keys, and adds a step for each index
// the table declares.
func (p *planner) create(index int, table *protocol.PlanningTable) error {
	desired, subject := table.GetDesired(), table.GetSubject()
	if desired == nil || table.GetCurrent() != nil {
		return fmt.Errorf("a created table needs its declaration and no observation")
	}
	common := p.commonStep(subject, protocol.PlanningEffect_CREATE, "table")
	if common == nil {
		return fmt.Errorf("the host planned no creation of table %s", phrase(subject))
	}
	if reason := SchemaRefusal("table", subject.GetSchema().GetSource(), subject.GetName().GetSource(), p.request.GetTargetFeatures()); reason != "" {
		return unsupported(index, "%s", reason)
	}
	if len(UniqueConstraints(desired)) > 0 && !slices.Contains(p.request.GetTargetFeatures(), provider.ObservedUniqueConstraintsFeature) {
		return unsupported(index, "table %s declares a UNIQUE over several columns, which a read reports only to a host that reads %s",
			phrase(subject), provider.ObservedUniqueConstraintsFeature)
	}
	held, err := p.declared.Hold(desired)
	if err != nil {
		return unsupported(index, "table %s: %v", phrase(subject), err)
	}
	operation, err := CreateTable(desired, held)
	if err != nil {
		return unsupported(index, "table %s: %v", phrase(subject), err)
	}
	step, err := p.replace(index, common, rankTable, operation, protocol.NativeOperation_STATEMENT, p.declared.CreationNotes(subject, desired))
	if err != nil {
		return err
	}
	p.created[keyOf(subject)] = step
	for _, declared := range desired.GetIndexes() {
		id, err := p.add(index, subject, rankIndexCreate, IndexOperation(declared), nil, readTable(subject),
			effect(indexSubject(subject, declared.GetName()), protocol.PlanningEffect_CREATE))
		if err != nil {
			return err
		}
		p.depend(step, id)
	}
	return nil
}

// planDrops replaces the host's removal of each table the plan drops. A
// table a kept table's foreign key references is refused: DuckDB refuses to
// drop it ("this table is main key table"). The tables of one schema whose
// foreign keys reference one another are dropped by one step, referencing
// tables first: the host keeps its removals in a sequence of its own order,
// which an order between separate steps could contradict, and edges inside
// one rewrite belong to its replacement. Every other table is dropped by a
// step of its own.
func (p *planner) planDrops() error {
	groups := make(map[string][]int)
	var schemas []string
	for i, table := range p.request.GetTables() {
		if table.GetAction() != protocol.PlanningTable_DROP {
			continue
		}
		if err := p.checkDrop(i, table); err != nil {
			return err
		}
		schema := keyOf(table.GetSubject()).schema
		if _, found := groups[schema]; !found {
			schemas = append(schemas, schema)
		}
		groups[schema] = append(groups[schema], i)
	}
	for _, schema := range schemas {
		members := groups[schema]
		if !p.related(members) {
			for _, i := range members {
				if err := p.dropOne(i); err != nil {
					return err
				}
			}
			continue
		}
		if err := p.dropGroup(p.dropOrder(members)); err != nil {
			return err
		}
	}
	return nil
}

// checkDrop refuses the removal of a table a kept table's foreign key
// references, and a removal the host did not plan as one step.
func (p *planner) checkDrop(index int, table *protocol.PlanningTable) error {
	subject := table.GetSubject()
	if table.GetCurrent() == nil {
		return fmt.Errorf("a dropped table needs its observation")
	}
	if p.commonStep(subject, protocol.PlanningEffect_DROP, "table") == nil {
		return fmt.Errorf("the host planned no removal of table %s", phrase(subject))
	}
	for _, other := range p.request.GetTables() {
		if other.GetAction() == protocol.PlanningTable_DROP || proto.Equal(other.GetSubject(), subject) {
			continue
		}
		if references(other.GetCurrent(), subject) {
			return unsupported(index, "dropping table %s: the foreign key of table %s references it, and DuckDB drops no table a foreign key references",
				phrase(subject), phrase(other.GetSubject()))
		}
	}
	return nil
}

// related reports whether a foreign key of one of the tables references
// another of them.
func (p *planner) related(members []int) bool {
	tables := p.request.GetTables()
	for _, i := range members {
		for _, j := range members {
			if i != j && references(tables[i].GetCurrent(), tables[j].GetSubject()) {
				return true
			}
		}
	}
	return false
}

// dropOrder orders tables the plan drops so each comes after every table
// whose foreign key references it, keeping the request's order otherwise.
// DuckDB creates a foreign key only to a table that exists, so the keys form
// no cycle; a table that references itself orders nothing.
func (p *planner) dropOrder(members []int) []int {
	tables := p.request.GetTables()
	remaining := slices.Clone(members)
	var order []int
	for len(remaining) > 0 {
		next := slices.IndexFunc(remaining, func(i int) bool {
			return !slices.ContainsFunc(remaining, func(j int) bool { return j != i && references(tables[j].GetCurrent(), tables[i].GetSubject()) })
		})
		next = max(next, 0)
		order = append(order, remaining[next])
		remaining = slices.Delete(remaining, next, next+1)
	}
	return order
}

// dropOne replaces the host's removal of one table with DROP TABLE, which
// drops the table's indexes too.
func (p *planner) dropOne(index int) error {
	subject := p.request.GetTables()[index].GetSubject()
	step, err := p.replace(index, p.commonStep(subject, protocol.PlanningEffect_DROP, "table"), rankTable, tableop.Operation{Action: tableop.DropTable},
		protocol.NativeOperation_STATEMENT, nil)
	if err != nil {
		return err
	}
	p.dropped[keyOf(subject)] = step
	return nil
}

// dropGroup replaces the host's removals of the tables, in order, with one
// step that drops them in that order, and which stands for every removal.
func (p *planner) dropGroup(order []int) error {
	tables := p.request.GetTables()
	first := tables[order[0]].GetSubject()
	operation := tableop.Operation{Action: tableop.DropTables}
	rewrite := &protocol.PlanningRewrite{}
	metadata := &protocol.PlanningMetadata{Transaction: protocol.PlanningMetadata_ALLOWED}
	for _, i := range order {
		subject := tables[i].GetSubject()
		operation.Tables = append(operation.Tables, subject.GetName().GetSource())
		common := p.commonStep(subject, protocol.PlanningEffect_DROP, "table")
		rewrite.Sources = append(rewrite.Sources, proto.CloneOf(common.GetId()))
		for _, change := range common.GetMetadata().GetEffects() {
			metadata.Effects = append(metadata.Effects, proto.CloneOf(change))
		}
	}
	id := p.stepID(rankTable, first)
	value, err := tableop.Capture(id.GetName(), owner(first), operation)
	if err != nil {
		return err
	}
	metadata.Impact, metadata.Reason = value.GetOperationSemantics().GetImpact(), value.GetOperationSemantics().GetReason()
	p.contribution.Steps = append(p.contribution.Steps, &protocol.PlanningStep{Id: id, Metadata: metadata,
		Operation: planningOperation(first, value, protocol.NativeOperation_STATEMENT, nil)})
	rewrite.Replacement = proto.CloneOf(id)
	p.result.Rewrites = append(p.result.Rewrites, rewrite)
	for _, i := range order {
		p.receipts[i] = append(p.receipts[i], proto.CloneOf(id))
		p.dropped[keyOf(tables[i].GetSubject())] = id
	}
	return nil
}

// order adds the dependencies between created tables: a created table after
// the created tables its foreign keys reference, which a cycle cannot
// satisfy. Dropped tables that reference one another share one step; see
// planDrops.
func (p *planner) order() error {
	for i, table := range p.request.GetTables() {
		if table.GetAction() != protocol.PlanningTable_CREATE {
			continue
		}
		subject := table.GetSubject()
		keys, err := ForeignKeys(table.GetDesired())
		if err != nil {
			return unsupported(i, "table %s: %v", phrase(subject), err)
		}
		for _, key := range keys {
			referenced := tableKey{schema: keyOf(subject).schema, name: foldASCII(key.RefTable)}
			if creation, found := p.created[referenced]; found && referenced != keyOf(subject) {
				if p.reaches(referenced, keyOf(subject), make(map[tableKey]bool)) {
					return unsupported(i, "table %s and table %q reference each other, and DuckDB creates a foreign key only to a table that exists",
						phrase(subject), key.RefTable)
				}
				p.depend(creation, p.created[keyOf(subject)])
			}
		}
	}
	return nil
}

// reaches reports whether a created table's foreign keys reach target,
// directly or through other created tables.
func (p *planner) reaches(from, target tableKey, seen map[tableKey]bool) bool {
	if seen[from] {
		return false
	}
	seen[from] = true
	table := p.tables[from]
	if table == nil || table.GetAction() != protocol.PlanningTable_CREATE {
		return false
	}
	keys, err := ForeignKeys(table.GetDesired())
	if err != nil {
		return false
	}
	for _, key := range keys {
		next := tableKey{schema: from.schema, name: foldASCII(key.RefTable)}
		if next == target || p.reaches(next, target, seen) {
			return true
		}
	}
	return false
}

// references reports whether an observed table holds a foreign key into the
// table subject names, which DuckDB keeps in the same schema.
func references(current *protocol.ObservedTable, subject *protocol.ComparisonIdentity) bool {
	if current == nil || foldASCII(schemaOfObserved(current)) != keyOf(subject).schema {
		return false
	}
	for _, constraint := range current.GetConstraints() {
		if strings.EqualFold(constraint.GetType(), "FOREIGN KEY") && foldASCII(constraint.GetForeignTable()) == keyOf(subject).name {
			return true
		}
	}
	return false
}

// claimRest refuses a host step of a table the service plans that no step
// replaced, naming it, rather than leave it for the host to refuse without
// the provider's words.
func (p *planner) claimRest() error {
	claimed := make(map[string]bool)
	for _, rewrite := range p.result.GetRewrites() {
		for _, source := range rewrite.GetSources() {
			claimed[source.GetName()] = true
		}
	}
	for _, common := range p.request.GetCommonSteps() {
		parent := common.GetParent()
		if parent == nil || common.GetCommonObject() != nil || claimed[common.GetId().GetName()] {
			continue
		}
		table, found := p.tables[keyOf(parent)]
		if !found || table.GetAction() == protocol.PlanningTable_CONTEXT {
			continue
		}
		return fmt.Errorf("the provider left the host step %s of table %s unplanned", common.GetId().GetName(), phrase(parent))
	}
	return nil
}

// commonStep finds the host step that does action to the table subject
// itself.
func (p *planner) commonStep(subject *protocol.ComparisonIdentity, action protocol.PlanningEffect_Action, kind string) *protocol.CommonPlanningStep {
	for _, common := range p.request.GetCommonSteps() {
		if !proto.Equal(common.GetParent(), subject) || common.GetCommonObject() != nil {
			continue
		}
		for _, change := range common.GetMetadata().GetEffects() {
			if change.GetAction() == action && change.GetSubject().GetKind() == kind && proto.Equal(change.GetSubject(), subject) {
				return common
			}
		}
	}
	return nil
}

// replace adds a step that does operation in place of the host step common,
// with that step's effects.
func (p *planner) replace(index int, common *protocol.CommonPlanningStep, rank string, operation tableop.Operation, role protocol.NativeOperation_Role,
	notes []string,
) (*protocol.StepIdentity, error) {
	subject := p.request.GetTables()[index].GetSubject()
	id := p.stepID(rank, subject)
	value, err := tableop.Capture(id.GetName(), owner(subject), operation)
	if err != nil {
		return nil, err
	}
	metadata := proto.CloneOf(common.GetMetadata())
	metadata.Transaction = protocol.PlanningMetadata_ALLOWED
	metadata.Impact, metadata.Reason = value.GetOperationSemantics().GetImpact(), value.GetOperationSemantics().GetReason()
	p.contribution.Steps = append(p.contribution.Steps, &protocol.PlanningStep{Id: id, Metadata: metadata, Operation: planningOperation(subject, value, role, notes)})
	p.result.Rewrites = append(p.result.Rewrites, &protocol.PlanningRewrite{Sources: []*protocol.StepIdentity{proto.CloneOf(common.GetId())}, Replacement: proto.CloneOf(id)})
	p.receipts[index] = append(p.receipts[index], proto.CloneOf(id))
	return id, nil
}

// add adds a step that does operation to the table subject beside the host's
// steps, with effects.
func (p *planner) add(index int, subject *protocol.ComparisonIdentity, rank string, operation tableop.Operation, notes []string,
	effects ...*protocol.PlanningEffect,
) (*protocol.StepIdentity, error) {
	id := p.stepID(rank, subject)
	value, err := tableop.Capture(id.GetName(), owner(subject), operation)
	if err != nil {
		return nil, err
	}
	role := protocol.NativeOperation_ALTER_TABLE
	switch operation.Action {
	case tableop.CreateIndex, tableop.DropIndex:
		role = protocol.NativeOperation_STATEMENT
	}
	metadata := &protocol.PlanningMetadata{Transaction: protocol.PlanningMetadata_ALLOWED, Effects: effects,
		Impact: value.GetOperationSemantics().GetImpact(), Reason: value.GetOperationSemantics().GetReason()}
	p.contribution.Steps = append(p.contribution.Steps, &protocol.PlanningStep{Id: id, Metadata: metadata, Operation: planningOperation(subject, value, role, notes)})
	p.receipts[index] = append(p.receipts[index], proto.CloneOf(id))
	return id, nil
}

// stepID names the next step on the table subject under rank. The step
// count keeps names distinct and ordered as they were planned.
func (p *planner) stepID(rank string, subject *protocol.ComparisonIdentity) *protocol.StepIdentity {
	return &protocol.StepIdentity{Owner: tableop.ProviderID,
		Name: fmt.Sprintf("%s/%04d/%s.%s", rank, len(p.contribution.GetSteps()), subject.GetSchema().GetSource(), subject.GetName().GetSource())}
}

func (p *planner) depend(before, after *protocol.StepIdentity) {
	if before == nil || after == nil {
		return
	}
	p.contribution.Dependencies = append(p.contribution.Dependencies, &protocol.PlanningDependency{Before: proto.CloneOf(before), After: proto.CloneOf(after)})
}

// planningOperation is the operation of a step. Every step runs in the
// lifecycle phase, which spans the host's whole sequence, so its effects and
// dependencies alone place it.
func planningOperation(subject *protocol.ComparisonIdentity, value *protocol.NormalizedValue, role protocol.NativeOperation_Role, notes []string) *protocol.PlanningOperation {
	operation := &protocol.PlanningOperation{Value: value, Role: role, Phase: protocol.PlanningOperation_LIFECYCLE, Notes: notes}
	if role == protocol.NativeOperation_ALTER_TABLE {
		operation.Parent = proto.CloneOf(subject)
	}
	return operation
}

func owner(subject *protocol.ComparisonIdentity) *protocol.Reference {
	schema := subject.GetSchema().GetSource()
	if schema == "" {
		schema = DefaultSchema
	}
	return tableop.Owner(schema, subject.GetName().GetSource())
}

func keyOf(subject *protocol.ComparisonIdentity) tableKey {
	schema := subject.GetSchema().GetSource()
	if schema == "" {
		schema = DefaultSchema
	}
	return tableKey{schema: foldASCII(schema), name: foldASCII(subject.GetName().GetSource())}
}

// schemaOfObserved is an observed table's schema, main when it names none.
func schemaOfObserved(table *protocol.ObservedTable) string {
	if table.GetSchema() == "" {
		return DefaultSchema
	}
	return table.GetSchema()
}

// foldASCII lowers the ASCII letters of name, as DuckDB compares names.
func foldASCII(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, name)
}

// phrase names a table for a message.
func phrase(subject *protocol.ComparisonIdentity) string {
	if schema := subject.GetSchema().GetSource(); schema != "" && foldASCII(schema) != DefaultSchema {
		return fmt.Sprintf("%q.%q", schema, subject.GetName().GetSource())
	}
	return fmt.Sprintf("%q", subject.GetName().GetSource())
}

func readTable(subject *protocol.ComparisonIdentity) *protocol.PlanningEffect {
	return effect(subject, protocol.PlanningEffect_READ)
}

func effect(subject *protocol.ComparisonIdentity, action protocol.PlanningEffect_Action) *protocol.PlanningEffect {
	return &protocol.PlanningEffect{Subject: proto.CloneOf(subject), Action: action}
}

// childSubject is the identity of a column of the table subject, built as the
// host builds one: the table's database, catalog and schema, the table's name
// as the parent, and the column's name.
func childSubject(subject *protocol.ComparisonIdentity, kind, name string) *protocol.ComparisonIdentity {
	child := indexSubject(subject, name)
	child.Kind, child.Parent = kind, proto.CloneOf(subject.GetName())
	return child
}

// indexSubject is the identity of an index of the table subject. An index
// name is unique in its schema, so the identity names no parent table.
func indexSubject(subject *protocol.ComparisonIdentity, name string) *protocol.ComparisonIdentity {
	return &protocol.ComparisonIdentity{Kind: "index", Database: proto.CloneOf(subject.GetDatabase()), Catalog: proto.CloneOf(subject.GetCatalog()),
		Schema: proto.CloneOf(subject.GetSchema()), Name: &protocol.ComparisonPart{Source: name, Normalized: foldASCII(name)}}
}

func cloneSteps(steps []*protocol.StepIdentity) []*protocol.StepIdentity {
	result := make([]*protocol.StepIdentity, len(steps))
	for i, step := range steps {
		result[i] = proto.CloneOf(step)
	}
	return result
}
