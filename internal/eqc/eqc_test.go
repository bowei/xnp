package eqc

import "testing"

type testObj struct {
	id     uint64
	ns     NsID
	labels LabelSet
}

func (o testObj) ID() uint64       { return o.id }
func (o testObj) Namespace() NsID  { return o.ns }
func (o testObj) Labels() LabelSet { return o.labels }

const (
	keyApp KeyID = iota + 1
	keyEnv
)

const (
	valWeb ValueID = iota + 1
	valDB
	valProd
	valStaging
)

const (
	ns1 NsID = iota + 1
	ns2
)

func TestAdd_CollapsesByRelevantLabels(t *testing.T) {
	c := New([]KeyID{keyApp})

	o1 := testObj{id: 1, ns: ns1, labels: MakeLabelSet(keyApp, valWeb, keyEnv, valProd)}
	o2 := testObj{id: 2, ns: ns1, labels: MakeLabelSet(keyApp, valWeb, keyEnv, valStaging)}
	o3 := testObj{id: 3, ns: ns1, labels: MakeLabelSet(keyApp, valDB, keyEnv, valProd)}
	c.Add([]Labeled{o1, o2, o3})

	partitions := c.partitions[ns1]
	if len(partitions) != 2 {
		t.Fatalf("got %d partitions, want 2 (app=web, app=db)", len(partitions))
	}

	var sizes []int
	for _, p := range partitions {
		sizes = append(sizes, len(p.Objects()))
	}
	got := map[int]int{}
	for _, s := range sizes {
		got[s]++
	}
	if got[2] != 1 || got[1] != 1 {
		t.Fatalf("partition sizes = %v, want one of size 2 and one of size 1", sizes)
	}
}

func TestAdd_NamespaceIsolation(t *testing.T) {
	c := New([]KeyID{keyApp})

	o1 := testObj{id: 1, ns: ns1, labels: MakeLabelSet(keyApp, valWeb)}
	o2 := testObj{id: 2, ns: ns2, labels: MakeLabelSet(keyApp, valWeb)}
	c.Add([]Labeled{o1, o2})

	if len(c.partitions) != 2 {
		t.Fatalf("got %d namespaces, want 2", len(c.partitions))
	}

	p1 := c.partitions[ns1]
	p2 := c.partitions[ns2]
	if len(p1) != 1 || len(p2) != 1 {
		t.Fatalf("expected exactly one partition per namespace, got ns1=%d ns2=%d", len(p1), len(p2))
	}

	var id1, id2 LabelSetID
	for id := range p1 {
		id1 = id
	}
	for id := range p2 {
		id2 = id
	}
	if id1 != id2 {
		t.Errorf("identical labels across namespaces got different LabelSetIDs: %d vs %d", id1, id2)
	}
}

func TestRemove(t *testing.T) {
	c := New([]KeyID{keyApp})

	o1 := testObj{id: 1, ns: ns1, labels: MakeLabelSet(keyApp, valWeb)}
	o2 := testObj{id: 2, ns: ns1, labels: MakeLabelSet(keyApp, valWeb)}
	c.Add([]Labeled{o1, o2})

	c.Remove([]Labeled{o1})
	partitions := c.partitions[ns1]
	if len(partitions) != 1 {
		t.Fatalf("got %d partitions after removing one of two objects, want 1", len(partitions))
	}
	for _, p := range partitions {
		if len(p.Objects()) != 1 || p.Objects()[0].ID() != 2 {
			t.Fatalf("remaining partition objects = %v, want just object 2", p.Objects())
		}
	}

	c.Remove([]Labeled{o2})
	if _, ok := c.partitions[ns1]; ok {
		t.Fatalf("namespace ns1 still present after removing all objects")
	}
}

func TestUpdateRelevant_MergesPartitions(t *testing.T) {
	c := New([]KeyID{keyApp, keyEnv})

	o1 := testObj{id: 1, ns: ns1, labels: MakeLabelSet(keyApp, valWeb, keyEnv, valProd)}
	o2 := testObj{id: 2, ns: ns1, labels: MakeLabelSet(keyApp, valWeb, keyEnv, valStaging)}
	c.Add([]Labeled{o1, o2})

	if len(c.partitions[ns1]) != 2 {
		t.Fatalf("got %d partitions before UpdateRelevant, want 2", len(c.partitions[ns1]))
	}

	c.UpdateRelevant([]KeyID{keyApp})

	partitions := c.partitions[ns1]
	if len(partitions) != 1 {
		t.Fatalf("got %d partitions after dropping keyEnv, want 1 (merged)", len(partitions))
	}
	for _, p := range partitions {
		if len(p.Objects()) != 2 {
			t.Fatalf("merged partition has %d objects, want 2", len(p.Objects()))
		}
	}
}

func TestUpdateRelevant_SplitsPartitions(t *testing.T) {
	c := New([]KeyID{keyApp})

	o1 := testObj{id: 1, ns: ns1, labels: MakeLabelSet(keyApp, valWeb, keyEnv, valProd)}
	o2 := testObj{id: 2, ns: ns1, labels: MakeLabelSet(keyApp, valWeb, keyEnv, valStaging)}
	c.Add([]Labeled{o1, o2})

	if len(c.partitions[ns1]) != 1 {
		t.Fatalf("got %d partitions before UpdateRelevant, want 1", len(c.partitions[ns1]))
	}

	c.UpdateRelevant([]KeyID{keyApp, keyEnv})

	partitions := c.partitions[ns1]
	if len(partitions) != 2 {
		t.Fatalf("got %d partitions after adding keyEnv, want 2 (split)", len(partitions))
	}
	for _, p := range partitions {
		if len(p.Objects()) != 1 {
			t.Fatalf("split partition has %d objects, want 1", len(p.Objects()))
		}
	}
}
