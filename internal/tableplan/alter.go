package tableplan

import (
	"fmt"
	"slices"
	"strings"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"google.golang.org/protobuf/proto"

	"github.com/stokaro/ptah-duckdb-plugin/internal/catalog"
	"github.com/stokaro/ptah-duckdb-plugin/internal/tableop"
)

// alteration is what a kept table's plan holds, before it becomes steps.
type alteration struct {
	index   int
	subject *protocol.ComparisonIdentity
	desired *protocol.DeclaredTable
	current *protocol.ObservedTable
	// held is the declaration as DuckDB holds it.
	held catalog.Table
	// The host's steps of the table by what they stand for.
	indexDrops, indexCreates, columnDrops, columnAdds []*protocol.CommonPlanningStep
}

// alter plans a table the plan keeps: it claims the host's steps that add and
// drop its columns and indexes, and adds a step for each change of a column's
// type, default and nullability, of the table's key and of its UNIQUE
// columns. It refuses every change DuckDB cannot make on a table that exists.
func (p *planner) alter(index int, table *protocol.PlanningTable) error {
	desired, current, subject := table.GetDesired(), table.GetCurrent(), table.GetSubject()
	if desired == nil || current == nil {
		return fmt.Errorf("an altered table needs its declaration and its observation")
	}
	held, err := p.declared.Hold(desired)
	if err != nil {
		return unsupported(index, "table %s: %v", phrase(subject), err)
	}
	a := &alteration{index: index, subject: subject, desired: desired, current: current, held: held}
	if err := p.sortHostSteps(a); err != nil {
		return err
	}
	if err := a.checkConstraints(); err != nil {
		return err
	}
	changes, err := a.columnChanges()
	if err != nil {
		return err
	}
	if err := p.checkDependents(a, changes); err != nil {
		return err
	}
	if err := a.checkDroppedIndexes(changes); err != nil {
		return err
	}
	return p.emit(a, changes)
}

// sortHostSteps sorts the host's steps of an altered table by what each one
// stands for, and refuses one the provider does not plan.
func (p *planner) sortHostSteps(a *alteration) error {
	for _, common := range p.request.GetCommonSteps() {
		if !proto.Equal(common.GetParent(), a.subject) || common.GetCommonObject() != nil {
			continue
		}
		if common.GetRenamedIndex() != nil {
			return unsupported(a.index, "table %s: DuckDB renames no index; drop index %q and create the new one", phrase(a.subject),
				common.GetRenamedIndex().GetFrom())
		}
		if common.GetAddedColumn() != nil {
			a.columnAdds = append(a.columnAdds, common)
			continue
		}
		sorted := false
		for _, change := range common.GetMetadata().GetEffects() {
			target := change.GetSubject()
			switch {
			case target.GetKind() == "column" && change.GetAction() == protocol.PlanningEffect_DROP:
				a.columnDrops, sorted = append(a.columnDrops, common), true
			case target.GetKind() == "index" && change.GetAction() == protocol.PlanningEffect_DROP:
				a.indexDrops, sorted = append(a.indexDrops, common), true
			case target.GetKind() == "index" && change.GetAction() == protocol.PlanningEffect_CREATE:
				a.indexCreates, sorted = append(a.indexCreates, common), true
			}
			if sorted {
				break
			}
		}
		if !sorted {
			return fmt.Errorf("table %s has a host step the provider does not plan", phrase(a.subject))
		}
	}
	return nil
}

// target is the subject a host step drops or creates, of kind.
func target(common *protocol.CommonPlanningStep, kind string) string {
	for _, change := range common.GetMetadata().GetEffects() {
		if change.GetSubject().GetKind() == kind && change.GetAction() != protocol.PlanningEffect_READ {
			return change.GetSubject().GetName().GetSource()
		}
	}
	return ""
}

// droppedColumns are the columns the host drops.
func (a *alteration) droppedColumns() []string {
	names := make([]string, 0, len(a.columnDrops))
	for _, common := range a.columnDrops {
		names = append(names, target(common, "column"))
	}
	return names
}

// remainingIndexes are the indexes the table holds that the plan keeps.
func (a *alteration) remainingIndexes() []string {
	var dropped []string
	for _, common := range a.indexDrops {
		dropped = append(dropped, foldASCII(target(common, "index")))
	}
	var remaining []string
	for _, index := range a.current.GetIndexes() {
		if !slices.Contains(dropped, foldASCII(index.GetName())) {
			remaining = append(remaining, index.GetName())
		}
	}
	return remaining
}

// checkConstraints compares a kept table's CHECKs and foreign keys with the
// ones it holds by what they say, and refuses a difference: DuckDB adds and
// drops neither on a table that exists. A CHECK that reads only columns the
// plan drops goes with them, as DuckDB drops it. Its primary key may only
// appear where the table has none, and its UNIQUE columns may only grow.
func (a *alteration) checkConstraints() error {
	dropped := foldAll(a.droppedColumns())
	var heldExpressions, declaredExpressions []string
	for _, constraint := range a.current.GetConstraints() {
		if !strings.EqualFold(constraint.GetType(), "CHECK") {
			continue
		}
		columns := foldAll(constraint.GetColumnNames())
		if len(columns) > 0 && !slices.ContainsFunc(columns, func(column string) bool { return !slices.Contains(dropped, column) }) {
			continue
		}
		heldExpressions = append(heldExpressions, constraint.GetCheckClause())
	}
	for _, check := range a.held.Checks {
		declaredExpressions = append(declaredExpressions, check.Expression)
	}
	slices.Sort(heldExpressions)
	slices.Sort(declaredExpressions)
	if !slices.Equal(heldExpressions, declaredExpressions) {
		return unsupported(a.index, "table %s: the declaration changes the table's CHECK constraints, and DuckDB adds and drops no CHECK on a table that exists",
			phrase(a.subject))
	}
	declaredKeys, err := ForeignKeys(a.desired)
	if err != nil {
		return unsupported(a.index, "table %s: %v", phrase(a.subject), err)
	}
	heldShapes := heldKeys(a.current)
	if len(declaredKeys) != len(heldShapes) {
		return unsupported(a.index, "table %s: the declaration changes the table's foreign keys, and DuckDB adds and drops no foreign key on a table that exists",
			phrase(a.subject))
	}
	for _, key := range declaredKeys {
		shape := keyShape{columns: foldAll(key.Columns), table: foldASCII(key.RefTable), refColumns: foldAll(key.RefColumns)}
		if !slices.ContainsFunc(heldShapes, shape.equal) {
			return unsupported(a.index, "table %s: the declaration adds the foreign key %s, and DuckDB adds no foreign key to a table that exists",
				phrase(a.subject), shape)
		}
	}
	declaredKey, heldKey := foldAll(PrimaryKey(a.desired)), foldAll(heldPrimaryKey(a.current))
	if !slices.Equal(declaredKey, heldKey) && len(heldKey) > 0 {
		return unsupported(a.index, "changing the primary key of table %s from (%s) to (%s): DuckDB changes and drops no primary key",
			phrase(a.subject), strings.Join(heldKey, ", "), strings.Join(declaredKey, ", "))
	}
	for _, column := range a.current.GetColumns() {
		if declared := heldColumn(a.held, column.GetName()); column.GetUnique() && declared != nil && !declared.Unique {
			return unsupported(a.index, "table %s: the declaration drops the UNIQUE constraint of column %q, and DuckDB drops no UNIQUE constraint",
				phrase(a.subject), column.GetName())
		}
	}
	return nil
}

// columnChange is one statement a kept column needs.
type columnChange struct {
	column    string
	operation tableop.Operation
	// alters is set for a change DuckDB refuses while the table has an index
	// or a foreign key of another table references it.
	alters bool
}

// columnChanges lists the statements that give each kept column its declared
// type, default and nullability, make it UNIQUE, and add the table's key.
func (a *alteration) columnChanges() ([]columnChange, error) {
	var changes []columnChange
	for _, column := range a.held.Columns {
		current := observedColumn(a.current, column.Name)
		if current == nil {
			continue
		}
		name := current.GetName()
		switch {
		case !sameText(column.Generated, current.GeneratedExpression):
			return nil, unsupported(a.index, "table %s: the declaration changes generated column %q, and DuckDB alters no generated column", phrase(a.subject), name)
		case column.Collation != current.GetCollation():
			return nil, unsupported(a.index, "table %s: the declaration changes the collation of column %q, which the provider does not plan", phrase(a.subject), name)
		}
		if column.Type != heldType(current) {
			if err := a.typeChangeAllowed(name); err != nil {
				return nil, err
			}
			changes = append(changes, columnChange{column: name, operation: tableop.Operation{Action: tableop.AlterColumnType, Name: name, Type: column.Type}, alters: true})
		}
		if !sameText(column.Default, current.DefaultExpression) {
			operation := tableop.Operation{Action: tableop.DropDefault, Name: name}
			if column.Default != nil {
				operation = tableop.Operation{Action: tableop.SetDefault, Name: name, Default: *column.Default}
			}
			changes = append(changes, columnChange{column: name, operation: operation})
		}
		if nullable := current.GetIsNullable() == "YES"; column.Nullable != nullable {
			action := tableop.SetNotNull
			if column.Nullable {
				action = tableop.DropNotNull
			}
			changes = append(changes, columnChange{column: name, operation: tableop.Operation{Action: action, Name: name}, alters: true})
		}
		if column.Unique && !current.GetUnique() {
			changes = append(changes, columnChange{column: name, operation: tableop.Operation{Action: tableop.AddUnique, Name: name}, alters: true})
		}
	}
	if key := PrimaryKey(a.desired); len(key) > 0 && len(heldPrimaryKey(a.current)) == 0 {
		changes = append(changes, columnChange{operation: tableop.Operation{Action: tableop.AddPrimaryKey, Key: slices.Clone(key)}, alters: true})
	}
	return changes, nil
}

// typeChangeAllowed refuses a type change of a column in a constraint, which
// DuckDB refuses ("Cannot change the type of a column that has a ...
// constraint specified").
func (a *alteration) typeChangeAllowed(column string) error {
	if slices.Contains(foldAll(heldPrimaryKey(a.current)), foldASCII(column)) {
		return unsupported(a.index, "changing the type of column %q of table %s: DuckDB changes the type of no column in a primary key", column, phrase(a.subject))
	}
	if current := observedColumn(a.current, column); current != nil && current.GetUnique() {
		return unsupported(a.index, "changing the type of column %q of table %s: DuckDB changes the type of no column with a UNIQUE constraint", column, phrase(a.subject))
	}
	for _, constraint := range a.current.GetConstraints() {
		kind := strings.ToUpper(constraint.GetType())
		if (kind == "CHECK" || kind == "FOREIGN KEY") && slices.Contains(foldAll(constraint.GetColumnNames()), foldASCII(column)) {
			return unsupported(a.index, "changing the type of column %q of table %s: DuckDB changes the type of no column in a %s constraint",
				column, phrase(a.subject), kind)
		}
	}
	return nil
}

// checkDependents refuses the changes DuckDB refuses while the table has an
// index or another table's foreign key references it, and a column removal
// DuckDB refuses where it stands.
func (p *planner) checkDependents(a *alteration, changes []columnChange) error {
	alters := len(a.columnDrops) > 0 || slices.ContainsFunc(changes, func(change columnChange) bool { return change.alters })
	for _, common := range a.columnAdds {
		if added := heldColumn(a.held, common.GetAddedColumn().GetName()); added != nil && (!added.Nullable || added.Unique) {
			alters = true
		}
	}
	if !alters {
		return nil
	}
	if remaining := a.remainingIndexes(); len(remaining) > 0 {
		return unsupported(a.index, "table %s: DuckDB changes no column's type, nullability or UNIQUE, and drops no column, while index %q exists; "+
			"drop the index in this change to make it", phrase(a.subject), remaining[0])
	}
	for _, other := range p.request.GetTables() {
		if other.GetAction() == protocol.PlanningTable_DROP || proto.Equal(other.GetSubject(), a.subject) {
			continue
		}
		if references(other.GetCurrent(), a.subject) {
			return unsupported(a.index, "table %s: DuckDB changes no column's type, nullability or UNIQUE, and drops no column, while the foreign key of table %s "+
				"references the table", phrase(a.subject), phrase(other.GetSubject()))
		}
	}
	for _, dropped := range a.droppedColumns() {
		if err := a.dropAllowed(dropped); err != nil {
			return err
		}
	}
	return nil
}

// checkDroppedIndexes refuses what DuckDB refuses in the transaction that
// drops an index: it keeps the index's storage until the transaction commits,
// so in that transaction it changes the type of no column the index covers,
// and drops no column the index covers nor one before such a column ("an
// index depends on it"). A key part that is an expression covers every
// column, since the provider does not read which columns it uses.
func (a *alteration) checkDroppedIndexes(changes []columnChange) error {
	columns := a.current.GetColumns()
	position := func(name string) int {
		return slices.IndexFunc(columns, func(column *protocol.ObservedColumn) bool { return foldASCII(column.GetName()) == foldASCII(name) })
	}
	for _, common := range a.indexDrops {
		name := target(common, "index")
		index := observedIndex(a.current, name)
		if index == nil {
			continue
		}
		var covered []int
		for _, part := range index.GetParts() {
			if part.GetColumn() == "" {
				covered = append(covered, len(columns)-1)
				continue
			}
			covered = append(covered, position(part.GetColumn()))
		}
		if len(covered) == 0 {
			continue
		}
		expression := slices.ContainsFunc(index.GetParts(), func(part *protocol.CapturedIndexPart) bool { return part.GetColumn() == "" })
		for _, change := range changes {
			if change.operation.Action != tableop.AlterColumnType || (!expression && !slices.Contains(covered, position(change.column))) {
				continue
			}
			return unsupported(a.index, "changing the type of column %q of table %s in the change that drops index %q: DuckDB keeps a dropped index "+
				"until the transaction commits, and changes the type of no column an index covers; drop the index in a change of its own first",
				change.column, phrase(a.subject), name)
		}
		for _, dropped := range a.droppedColumns() {
			if position(dropped) <= slices.Max(covered) {
				return unsupported(a.index, "dropping column %q of table %s in the change that drops index %q: DuckDB keeps a dropped index "+
					"until the transaction commits, and drops no column an index covers nor one before such a column; drop the index in a change of its own first",
					dropped, phrase(a.subject), name)
			}
		}
	}
	return nil
}

// observedIndex is the index of a table under name, or nil.
func observedIndex(table *protocol.ObservedTable, name string) *protocol.ObservedIndex {
	for _, index := range table.GetIndexes() {
		if foldASCII(index.GetName()) == foldASCII(name) {
			return index
		}
	}
	return nil
}

// dropAllowed refuses the removal of a column DuckDB refuses to drop: one in
// a primary key, a UNIQUE or a foreign key, and one that a primary key or
// UNIQUE column follows ("an index depends on a column after it").
func (a *alteration) dropAllowed(column string) error {
	keyed := foldAll(heldPrimaryKey(a.current))
	for _, constraint := range a.current.GetConstraints() {
		if strings.EqualFold(constraint.GetType(), "FOREIGN KEY") && slices.Contains(foldAll(constraint.GetColumnNames()), foldASCII(column)) {
			return unsupported(a.index, "dropping column %q of table %s: DuckDB drops no column in a foreign key", column, phrase(a.subject))
		}
	}
	position := slices.IndexFunc(a.current.GetColumns(), func(held *protocol.ObservedColumn) bool { return foldASCII(held.GetName()) == foldASCII(column) })
	for i, held := range a.current.GetColumns() {
		indexed := slices.Contains(keyed, foldASCII(held.GetName())) || held.GetUnique()
		switch {
		case indexed && i == position:
			return unsupported(a.index, "dropping column %q of table %s: DuckDB drops no column in a primary key or a UNIQUE constraint", column, phrase(a.subject))
		case indexed && i > position:
			return unsupported(a.index, "dropping column %q of table %s: DuckDB drops no column that the key or UNIQUE column %q follows",
				column, phrase(a.subject), held.GetName())
		}
	}
	return nil
}

// emit writes the steps of an altered table: the index removals, then the
// column changes and additions, then the index creations.
func (p *planner) emit(a *alteration, changes []columnChange) error {
	drops, err := p.emitIndexDrops(a)
	if err != nil {
		return err
	}
	middle, structural, err := p.emitColumns(a, changes)
	if err != nil {
		return err
	}
	creates, err := p.emitIndexCreates(a)
	if err != nil {
		return err
	}
	for _, step := range middle {
		for _, drop := range drops {
			p.depend(drop, step)
		}
		for _, create := range creates {
			p.depend(step, create)
		}
	}
	return p.orderReferences(a, structural)
}

// emitIndexDrops replaces the host's removal of each index with DROP INDEX.
func (p *planner) emitIndexDrops(a *alteration) ([]*protocol.StepIdentity, error) {
	var drops []*protocol.StepIdentity
	for _, common := range a.indexDrops {
		step, err := p.replace(a.index, common, rankIndexDrop, tableop.Operation{Action: tableop.DropIndex, Name: target(common, "index")}, protocol.NativeOperation_STATEMENT, nil)
		if err != nil {
			return nil, err
		}
		drops = append(drops, step)
	}
	return drops, nil
}

// emitColumns writes the column removals, additions and changes, and
// returns them with the structural ones among them: the steps DuckDB refuses
// while a foreign key of another table references the table.
func (p *planner) emitColumns(a *alteration, changes []columnChange) (middle, structural []*protocol.StepIdentity, err error) {
	for _, common := range a.columnDrops {
		step, err := p.replace(a.index, common, rankColumn, tableop.Operation{Action: tableop.DropColumn, Name: target(common, "column")}, protocol.NativeOperation_ALTER_TABLE, nil)
		if err != nil {
			return nil, nil, err
		}
		middle, structural = append(middle, step), append(structural, step)
	}
	for _, common := range a.columnAdds {
		steps, err := p.addColumn(a, common)
		if err != nil {
			return nil, nil, err
		}
		middle = append(middle, steps...)
		if added := heldColumn(a.held, common.GetAddedColumn().GetName()); added != nil && (!added.Nullable || added.Unique) {
			structural = append(structural, steps...)
		}
	}
	// previous is the last step that changes each column: two changes of one
	// column alter one subject, so they run in the order columnChanges lists
	// them.
	previous := make(map[string]*protocol.StepIdentity)
	for _, change := range changes {
		step, err := p.emitChange(a, change)
		if err != nil {
			return nil, nil, err
		}
		middle = append(middle, step)
		if change.alters {
			structural = append(structural, step)
		}
		if change.column != "" {
			p.depend(previous[foldASCII(change.column)], step)
			previous[foldASCII(change.column)] = step
		}
	}
	return middle, structural, nil
}

// emitChange adds the step of one column change, or of the table's new key.
func (p *planner) emitChange(a *alteration, change columnChange) (*protocol.StepIdentity, error) {
	var notes []string
	if declared := declaredColumn(a.desired, change.column); declared != nil && change.operation.Action == tableop.AlterColumnType {
		if note := LengthNote(a.subject, a.desired, declared); note != "" {
			notes = []string{note}
		}
	}
	subjectEffect := effect(a.subject, protocol.PlanningEffect_ALTER)
	if change.column != "" {
		subjectEffect = effect(childSubject(a.subject, "column", change.column), protocol.PlanningEffect_ALTER)
	}
	return p.add(a.index, a.subject, rankColumn, change.operation, notes, readTable(a.subject), subjectEffect)
}

// emitIndexCreates replaces the host's creation of each index with CREATE
// INDEX.
func (p *planner) emitIndexCreates(a *alteration) ([]*protocol.StepIdentity, error) {
	var creates []*protocol.StepIdentity
	for _, common := range a.indexCreates {
		name := target(common, "index")
		declared := declaredIndex(a.desired, name)
		if declared == nil {
			return nil, fmt.Errorf("table %s does not declare the index %q the host adds", phrase(a.subject), name)
		}
		step, err := p.replace(a.index, common, rankIndexCreate, IndexOperation(declared), protocol.NativeOperation_STATEMENT, nil)
		if err != nil {
			return nil, err
		}
		creates = append(creates, step)
	}
	return creates, nil
}

// orderReferences places the structural changes of an altered table between
// the tables whose foreign keys reference it: after the removal of each such
// table the plan drops, and before the creation of each such table it
// creates, since DuckDB refuses the change while either one exists. The host
// creates every table before it drops any, so a plan that needs both orders
// is refused by name.
func (p *planner) orderReferences(a *alteration, structural []*protocol.StepIdentity) error {
	if len(structural) == 0 {
		return nil
	}
	var dropped, created *protocol.PlanningTable
	for _, other := range p.request.GetTables() {
		key := keyOf(other.GetSubject())
		switch {
		case other.GetAction() == protocol.PlanningTable_DROP && references(other.GetCurrent(), a.subject):
			dropped = other
			for _, step := range structural {
				p.depend(p.dropped[key], step)
			}
		case other.GetAction() == protocol.PlanningTable_CREATE && declaresReference(other, a.subject):
			created = other
			for _, step := range structural {
				p.depend(step, p.created[key])
			}
		}
	}
	if dropped != nil && created != nil {
		return unsupported(a.index, "table %s: DuckDB changes no column's type, nullability or UNIQUE, and drops no column, while a foreign key references "+
			"the table, and the plan creates table %s, which references it, before it drops table %s; make the change and the new table in two plans",
			phrase(a.subject), phrase(created.GetSubject()), phrase(dropped.GetSubject()))
	}
	return nil
}

// declaresReference reports whether a created table declares a foreign key
// into the table subject names, in its own schema as DuckDB keeps it.
func declaresReference(table *protocol.PlanningTable, subject *protocol.ComparisonIdentity) bool {
	keys, err := ForeignKeys(table.GetDesired())
	if err != nil {
		return false
	}
	return slices.ContainsFunc(keys, func(key tableop.ForeignKey) bool {
		return tableKey{schema: keyOf(table.GetSubject()).schema, name: foldASCII(key.RefTable)} == keyOf(subject)
	})
}

// addColumn replaces the host's addition of a column with ADD COLUMN, and a
// UNIQUE column's constraint with ADD UNIQUE after it, since DuckDB adds no
// column with a constraint other than NOT NULL.
func (p *planner) addColumn(a *alteration, common *protocol.CommonPlanningStep) ([]*protocol.StepIdentity, error) {
	name := common.GetAddedColumn().GetName()
	held := heldColumn(a.held, name)
	if held == nil {
		return nil, fmt.Errorf("table %s does not declare the column %q the host adds", phrase(a.subject), name)
	}
	if held.Generated != nil {
		return nil, unsupported(a.index, "table %s: DuckDB adds no generated column to a table that exists; column %q", phrase(a.subject), name)
	}
	column := HeldColumn(*held)
	column.Unique = false
	var notes []string
	if declared := declaredColumn(a.desired, name); declared != nil {
		if note := LengthNote(a.subject, a.desired, declared); note != "" {
			notes = append(notes, note)
		}
	}
	if !column.Nullable && column.Default == nil {
		notes = append(notes, fmt.Sprintf("DUCKDB: ADD COLUMN %q NOT NULL fails if table %s holds a row; the column declares no default to fill it with.",
			name, phrase(a.subject)))
	}
	step, err := p.replace(a.index, common, rankColumn, tableop.Operation{Action: tableop.AddColumn, Column: &column}, protocol.NativeOperation_ALTER_TABLE, notes)
	if err != nil || !held.Unique {
		return []*protocol.StepIdentity{step}, err
	}
	unique, err := p.add(a.index, a.subject, rankColumn, tableop.Operation{Action: tableop.AddUnique, Name: name}, nil, readTable(a.subject),
		effect(childSubject(a.subject, "column", name), protocol.PlanningEffect_ALTER))
	if err != nil {
		return nil, err
	}
	p.depend(step, unique)
	return []*protocol.StepIdentity{step, unique}, nil
}

// sameText reports whether two optional texts are both absent or equal.
func sameText(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return strings.TrimSpace(*a) == strings.TrimSpace(*b)
}
