package eqc

import (
	"fmt"
	"math/rand"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unique"
)

// Benchmarks for the unique.Handle[string]-based UEqC prototype (eqc_unique.go),
// reusing eqc_bench_test.go's benchConfigs so results are directly comparable
// benchmark-by-benchmark (BenchmarkAdd/X vs BenchmarkUAdd/X, etc) against EqC's
// intern.ID-based implementation:
//
//	go test ./internal/eqc/ -run '^$' -bench 'Add$|UAdd$' -benchmem
//
// See eqc_unique.go's package doc comment for the conclusion.

type testUObj struct {
	id     uint64
	ns     NsID
	labels USet
}

func (o testUObj) ID() uint64      { return o.id }
func (o testUObj) Namespace() NsID { return o.ns }
func (o testUObj) Labels() USet    { return o.labels }

// genUObjects is genObjects's USet-based twin: same shapes (benchConfig),
// same deterministic seed and sampling logic, but real strings ("k3", "v42")
// standing in for the KeyID/ValueID ints genObjects packs directly — since
// the whole point of this variant is going straight from label text to
// unique.Handle, with no intern-to-int step first.
func genUObjects(cfg benchConfig) (objects []ULabeled, relevant []unique.Handle[string]) {
	rng := rand.New(rand.NewSource(1))

	for k := 0; k < cfg.numKeys; k++ {
		relevant = append(relevant, unique.Make(fmt.Sprintf("k%d", k+1)))
	}
	uniqueKey := fmt.Sprintf("k%d", cfg.numKeys+1)
	if cfg.forceUnique {
		relevant = append(relevant, unique.Make(uniqueKey))
	}
	noiseBase := cfg.numKeys + 2 // above uniqueKey, never collides

	var relevantValue func() string
	if cfg.skewed {
		zipf := rand.NewZipf(rng, 1.5, 1, uint64(cfg.cardinality-1))
		relevantValue = func() string { return fmt.Sprintf("v%d", 1+zipf.Uint64()) }
	} else {
		relevantValue = func() string { return fmt.Sprintf("v%d", 1+rng.Intn(cfg.cardinality)) }
	}

	objects = make([]ULabeled, cfg.numObjects)
	for i := 0; i < cfg.numObjects; i++ {
		kvs := make([]string, 0, 2*(cfg.numKeys+cfg.noiseKeys+1))
		for k := 0; k < cfg.numKeys; k++ {
			kvs = append(kvs, fmt.Sprintf("k%d", k+1), relevantValue())
		}
		if cfg.forceUnique {
			kvs = append(kvs, uniqueKey, fmt.Sprintf("v%d", i+1))
		}
		for k := 0; k < cfg.noiseKeys; k++ {
			kvs = append(kvs, fmt.Sprintf("k%d", noiseBase+k), fmt.Sprintf("v%d", 1+rng.Intn(cfg.cardinality)))
		}
		objects[i] = testUObj{
			id:     uint64(i + 1),
			ns:     NsID(1 + i%cfg.numNS),
			labels: MakeUSetKV(kvs...),
		}
	}
	return objects, relevant
}

func BenchmarkUAdd(b *testing.B) {
	for _, cfg := range benchConfigs {
		b.Run(cfg.name, func(b *testing.B) {
			objects, relevant := genUObjects(cfg)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c := NewUnique(relevant)
				c.Add(objects)
			}
			b.StopTimer()
			reportPerUObject(b, objects)
		})
	}
}

func BenchmarkUAddTwice(b *testing.B) {
	for _, cfg := range benchConfigs {
		b.Run(cfg.name, func(b *testing.B) {
			objects, relevant := genUObjects(cfg)
			mid := len(objects) / 2
			first, second := objects[:mid], objects[mid:]

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c := NewUnique(relevant)
				c.Add(first)
				c.Add(second)
			}
			b.StopTimer()
			reportPerUObject(b, objects)
		})
	}
}

// genUpdatedUObjects is genUpdatedObjects's USet twin: bumps every relevant
// key's value so each object lands in a different partition on Update.
func genUpdatedUObjects(cfg benchConfig, objects []ULabeled) []ULabeled {
	updated := make([]ULabeled, len(objects))
	for i, o := range objects {
		old := o.Labels()
		kvs := make([]string, 0, old.Len()*2)
		for j := 0; j < old.Len(); j++ {
			k, v := old.At(j)
			kStr, vStr := k.Value(), v.Value()
			if isRelevantNumericUKey(kStr, cfg.numKeys) {
				n, _ := strconv.Atoi(strings.TrimPrefix(vStr, "v"))
				vStr = fmt.Sprintf("v%d", n+1)
			}
			kvs = append(kvs, kStr, vStr)
		}
		to := o.(testUObj)
		to.labels = MakeUSetKV(kvs...)
		updated[i] = to
	}
	return updated
}

// isRelevantNumericUKey reports whether k is one of genUObjects's relevant
// "k1".."k<numKeys>" keys. USet sorts pairs by string content, not
// generation order, and string order doesn't match numeric order ("k10" <
// "k2"), so genUpdatedUObjects can't tell relevant keys apart by position
// the way genUpdatedObjects does with plain KeyID comparisons — it has to
// parse the key back out instead.
func isRelevantNumericUKey(k string, numKeys int) bool {
	if !strings.HasPrefix(k, "k") {
		return false
	}
	n, err := strconv.Atoi(k[1:])
	return err == nil && n >= 1 && n <= numKeys
}

func BenchmarkUUpdate(b *testing.B) {
	for _, cfg := range benchConfigs {
		b.Run(cfg.name, func(b *testing.B) {
			objects, relevant := genUObjects(cfg)
			updated := genUpdatedUObjects(cfg, objects)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				c := NewUnique(relevant)
				c.Add(objects)
				b.StartTimer()

				c.Update(updated)
			}
			b.StopTimer()
			reportPerUObject(b, objects)
		})
	}
}

func BenchmarkURemove(b *testing.B) {
	for _, cfg := range benchConfigs {
		b.Run(cfg.name, func(b *testing.B) {
			objects, relevant := genUObjects(cfg)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				c := NewUnique(relevant)
				c.Add(objects)
				b.StartTimer()

				c.Remove(objects)
			}
			b.StopTimer()
			reportPerUObject(b, objects)
		})
	}
}

func BenchmarkUUpdateRelevant(b *testing.B) {
	for _, cfg := range benchConfigs {
		b.Run(cfg.name, func(b *testing.B) {
			objects, relevant := genUObjects(cfg)
			altRelevant := relevant[:max(0, len(relevant)-1)]

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				c := NewUnique(relevant)
				c.Add(objects)
				b.StartTimer()

				c.UpdateRelevant(altRelevant)
			}
			b.StopTimer()
			reportPerUObject(b, objects)
		})
	}
}

func reportPerUObject(b *testing.B, objects []ULabeled) {
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(objects)), "ns/obj")
}

// TestUMemoryFootprint is TestMemoryFootprint's UEqC twin.
func TestUMemoryFootprint(t *testing.T) {
	for _, cfg := range benchConfigs {
		t.Run(cfg.name, func(t *testing.T) {
			objects, relevant := genUObjects(cfg)

			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)

			c := NewUnique(relevant)
			c.Add(objects)

			runtime.GC()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)

			bytesPerObj := float64(after.HeapAlloc-before.HeapAlloc) / float64(len(objects))
			t.Logf("%d objects -> %d distinct groups: %.1f bytes/object retained by UEqC", len(objects), len(c.labelSetByKey), bytesPerObj)
			runtime.KeepAlive(c)
		})
	}
}
