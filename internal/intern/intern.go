package intern

// ID is an interned string identity, allocated by Table.
type ID uint32

// Table interns strings into dense IDs so repeated values (namespace names
// foremost — every pod in a namespace carries its own decoded copy of the
// same string) collapse to one allocation, and comparisons in hot paths
// become integer equality.
type Table struct {
	ids    map[string]ID
	values []string
}

func NewTable() *Table {
	return &Table{ids: map[string]ID{}}
}

func (t *Table) Intern(s string) ID {
	if id, ok := t.ids[s]; ok {
		return id
	}
	id := ID(len(t.values))
	t.ids[s] = id
	t.values = append(t.values, s)
	return id
}

// String resolves id back to its original string.
func (t *Table) String(id ID) string {
	return t.values[id]
}

// Pair is a packed representation of key/value ID pair.
type Pair uint64

// NewPair packs a key/value ID pair into a Pair.
func NewPair(key, value ID) Pair { return Pair(key)<<32 | Pair(value) }

func (p Pair) Key() ID   { return ID(p >> 32) }
func (p Pair) Value() ID { return ID(uint32(p)) }
