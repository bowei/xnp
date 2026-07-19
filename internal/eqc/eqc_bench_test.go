package eqc

import (
	"math/rand"
	"runtime"
	"testing"
)

// benchConfig describes one synthetic input distribution. The axes that
// matter for EqC performance are: how many relevant keys are tracked, how
// many distinct values each key takes (low cardinality -> objects collapse
// into few shared partitions; high cardinality -> partitions approach one
// per object), how many namespaces spread the objects out, and how many
// irrelevant ("noise") labels ride along and must be filtered by Project.
type benchConfig struct {
	name        string
	numObjects  int
	numNS       int
	numKeys     int  // number of relevant label keys
	cardinality int  // distinct values per relevant key
	noiseKeys   int  // irrelevant labels per object, outside the relevant set
	forceUnique bool // append a relevant key whose value is unique per object
	skewed      bool // sample relevant-key values from a Zipfian distribution
	// instead of uniform, producing one dominant ("hot") partition plus a
	// long tail of rare, near-singleton ones in the same namespace.
}

var benchConfigs = []benchConfig{
	{name: "AllShared_1Key", numObjects: 20000, numNS: 1, numKeys: 1, cardinality: 1},
	{name: "SharedFewGroups_1Key", numObjects: 20000, numNS: 1, numKeys: 1, cardinality: 8},
	{name: "AllShared_8Keys", numObjects: 20000, numNS: 1, numKeys: 8, cardinality: 1},
	{name: "LowCardinality_4Keys", numObjects: 10000, numNS: 1, numKeys: 4, cardinality: 10},
	{name: "MediumCardinality_4Keys", numObjects: 5000, numNS: 1, numKeys: 4, cardinality: 50},
	{name: "HighCardinality_4Keys", numObjects: 2000, numNS: 1, numKeys: 4, cardinality: 500},
	{name: "AllUnique_4Keys", numObjects: 2000, numNS: 1, numKeys: 4, cardinality: 10, forceUnique: true},
	{name: "ManyNamespaces_Shared", numObjects: 20000, numNS: 200, numKeys: 2, cardinality: 4},
	{name: "ManyNamespaces_Unique", numObjects: 2000, numNS: 200, numKeys: 2, cardinality: 10, forceUnique: true},
	{name: "NoiseLabels_LowCardinality", numObjects: 10000, numNS: 1, numKeys: 4, cardinality: 10, noiseKeys: 20},

	// Pathological cases, each pushing one axis an order of magnitude past
	// the configs above: object count within a single namespace, per-key
	// value cardinality alone, value cardinality combined with key count,
	// and key count alone.
	{name: "LargeSingleNamespace", numObjects: 200000, numNS: 1, numKeys: 4, cardinality: 50},
	{name: "VeryHighCardinality_1Key", numObjects: 20000, numNS: 1, numKeys: 1, cardinality: 5000},
	{name: "VeryHighCardinality_KeysAndValues", numObjects: 20000, numNS: 1, numKeys: 8, cardinality: 5000},
	{name: "VeryHighCardinality_Keys", numObjects: 20000, numNS: 1, numKeys: 80, cardinality: 10},

	// Distinct pathological shapes, not just axis magnitude: a hot
	// partition amid a long tail, extreme namespace fragmentation, label
	// sets dominated by irrelevant noise, and one giant shared partition.
	{name: "SkewedCardinality_HotPartition", numObjects: 20000, numNS: 1, numKeys: 4, cardinality: 500, skewed: true},
	{name: "NamespaceFragmentation_OnePerNS", numObjects: 20000, numNS: 20000, numKeys: 2, cardinality: 4},
	{name: "NoiseDominated_ManyLabels", numObjects: 10000, numNS: 1, numKeys: 2, cardinality: 10, noiseKeys: 300},
	{name: "GiantSinglePartition", numObjects: 200000, numNS: 1, numKeys: 1, cardinality: 1},
}

// genObjects deterministically builds cfg.numObjects Labeled objects plus
// the relevant-key list to construct an EqC that partitions them.
func genObjects(cfg benchConfig) (objects []Labeled, relevant []KeyID) {
	rng := rand.New(rand.NewSource(1))

	for k := 0; k < cfg.numKeys; k++ {
		relevant = append(relevant, KeyID(k+1))
	}
	uniqueKey := KeyID(cfg.numKeys + 1)
	if cfg.forceUnique {
		relevant = append(relevant, uniqueKey)
	}
	noiseBase := KeyID(cfg.numKeys + 2) // above uniqueKey, never collides

	// A Zipfian sampler concentrates most draws on value 1 (the "hot"
	// value) with a long thin tail out to cardinality, instead of the
	// uniform.Intn spread used elsewhere — modeling the common real-world
	// case where most objects share one dominant label value (e.g. a
	// default) and a minority carry rare ones.
	var relevantValue func() ValueID
	if cfg.skewed {
		zipf := rand.NewZipf(rng, 1.5, 1, uint64(cfg.cardinality-1))
		relevantValue = func() ValueID { return ValueID(1 + zipf.Uint64()) }
	} else {
		relevantValue = func() ValueID { return ValueID(1 + rng.Intn(cfg.cardinality)) }
	}

	objects = make([]Labeled, cfg.numObjects)
	for i := 0; i < cfg.numObjects; i++ {
		kvs := make([]any, 0, 2*(cfg.numKeys+cfg.noiseKeys+1))
		for k := 0; k < cfg.numKeys; k++ {
			kvs = append(kvs, KeyID(k+1), relevantValue())
		}
		if cfg.forceUnique {
			kvs = append(kvs, uniqueKey, ValueID(i+1))
		}
		for k := 0; k < cfg.noiseKeys; k++ {
			kvs = append(kvs, noiseBase+KeyID(k), ValueID(1+rng.Intn(cfg.cardinality)))
		}
		objects[i] = testObj{
			id:     uint64(i + 1),
			ns:     NsID(1 + i%cfg.numNS),
			labels: MakeLabelSet(kvs...),
		}
	}
	return objects, relevant
}

func BenchmarkAdd(b *testing.B) {
	for _, cfg := range benchConfigs {
		b.Run(cfg.name, func(b *testing.B) {
			objects, relevant := genObjects(cfg)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c := New(relevant)
				c.Add(objects)
			}
			b.StopTimer()
			reportPerObject(b, objects)
		})
	}
}

// BenchmarkAddTwice times adding the same object set as two back-to-back
// batches instead of one, exercising the code path Add takes once
// c.partitions/c.labelSetByKey are already populated (existing namespace
// entries, already-interned label-set groups for every group the first
// batch already saw) rather than only the from-empty path BenchmarkAdd
// covers.
func BenchmarkAddTwice(b *testing.B) {
	for _, cfg := range benchConfigs {
		b.Run(cfg.name, func(b *testing.B) {
			objects, relevant := genObjects(cfg)
			mid := len(objects) / 2
			first, second := objects[:mid], objects[mid:]

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c := New(relevant)
				c.Add(first)
				c.Add(second)
			}
			b.StopTimer()
			reportPerObject(b, objects)
		})
	}
}

// genUpdatedObjects returns objects with the same IDs/namespaces but every
// relevant key's value bumped by one, so each object genuinely lands in a
// different partition when passed to Update (unlike a same-labels no-op
// update) regardless of cfg.cardinality. Object identity (K) is untouched,
// so LabelSet's sort-by-K invariant still holds without re-sorting.
func genUpdatedObjects(cfg benchConfig, objects []Labeled) []Labeled {
	updated := make([]Labeled, len(objects))
	for i, o := range objects {
		old := o.Labels()
		labels := make(LabelSet, len(old))
		copy(labels, old)
		for j := range labels {
			if int(labels[j].K()) <= cfg.numKeys {
				labels[j] = KV(labels[j].K(), labels[j].V()+1)
			}
		}
		to := o.(testObj)
		to.labels = labels
		updated[i] = to
	}
	return updated
}

// BenchmarkUpdate times c.Update on objects whose relevant labels have
// actually changed (as opposed to BenchmarkUpdateRelevant, which changes
// the tracked key set itself) — the evict-and-re-add path for every
// object, as distinct from Update's no-op fast path when labels are
// unchanged.
func BenchmarkUpdate(b *testing.B) {
	for _, cfg := range benchConfigs {
		b.Run(cfg.name, func(b *testing.B) {
			objects, relevant := genObjects(cfg)
			updated := genUpdatedObjects(cfg, objects)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				c := New(relevant)
				c.Add(objects)
				b.StartTimer()

				c.Update(updated)
			}
			b.StopTimer()
			reportPerObject(b, objects)
		})
	}
}

func BenchmarkRemove(b *testing.B) {
	for _, cfg := range benchConfigs {
		b.Run(cfg.name, func(b *testing.B) {
			objects, relevant := genObjects(cfg)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				c := New(relevant)
				c.Add(objects)
				b.StartTimer()

				c.Remove(objects)
			}
			b.StopTimer()
			reportPerObject(b, objects)
		})
	}
}

// BenchmarkUpdateRelevant times dropping the last relevant key, which
// forces every existing partition to be recomputed and merged with any
// other partition that now has identical projected labels.
func BenchmarkUpdateRelevant(b *testing.B) {
	for _, cfg := range benchConfigs {
		b.Run(cfg.name, func(b *testing.B) {
			objects, relevant := genObjects(cfg)
			altRelevant := relevant[:max(0, len(relevant)-1)]

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				c := New(relevant)
				c.Add(objects)
				b.StartTimer()

				c.UpdateRelevant(altRelevant)
			}
			b.StopTimer()
			reportPerObject(b, objects)
		})
	}
}

func reportPerObject(b *testing.B, objects []Labeled) {
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(objects)), "ns/obj")
}

// TestMemoryFootprint reports the steady-state heap bytes retained per
// object by EqC's own bookkeeping (partitions, the location index, and
// the label-set intern index) — not throughput, but standing memory
// cost, which matters as much as CPU at the object counts this is meant
// to scale to. Isolated from the input objects' own memory (built and
// GC-settled before the snapshot) so the number reflects EqC's
// overhead alone.
func TestMemoryFootprint(t *testing.T) {
	for _, cfg := range benchConfigs {
		t.Run(cfg.name, func(t *testing.T) {
			objects, relevant := genObjects(cfg)

			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)

			c := New(relevant)
			c.Add(objects)

			runtime.GC()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)

			bytesPerObj := float64(after.HeapAlloc-before.HeapAlloc) / float64(len(objects))
			t.Logf("%d objects -> %d distinct groups: %.1f bytes/object retained by EqC", len(objects), len(c.labelSetByKey), bytesPerObj)
			runtime.KeepAlive(c)
		})
	}
}
