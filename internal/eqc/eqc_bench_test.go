package eqc

import (
	"math/rand"
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

	objects = make([]Labeled, cfg.numObjects)
	for i := 0; i < cfg.numObjects; i++ {
		kvs := make([]any, 0, 2*(cfg.numKeys+cfg.noiseKeys+1))
		for k := 0; k < cfg.numKeys; k++ {
			kvs = append(kvs, KeyID(k+1), ValueID(1+rng.Intn(cfg.cardinality)))
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
