package repr

// StringID is an interned string identity, allocated by SymbolTable.
type StringID uint32

// SymbolTable interns strings into dense StringIDs so repeated values
// (namespace names foremost — every pod in a namespace carries its own
// decoded copy of the same string) collapse to one allocation, and
// comparisons in hot paths become integer equality.
type SymbolTable struct {
	ids    map[string]StringID
	values []string
}

func NewSymbolTable() *SymbolTable {
	return &SymbolTable{ids: map[string]StringID{}}
}

func (t *SymbolTable) Intern(s string) StringID {
	if id, ok := t.ids[s]; ok {
		return id
	}
	id := StringID(len(t.values))
	t.ids[s] = id
	t.values = append(t.values, s)
	return id
}

// String resolves id back to its original string.
func (t *SymbolTable) String(id StringID) string {
	return t.values[id]
}
