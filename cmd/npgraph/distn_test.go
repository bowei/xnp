package main

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

// testRand is the generator every distribution test draws from. It is seeded
// per test rather than shared, so a test's draws do not depend on whether
// another test ran first.
func testRand() *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }

func TestDistributionValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		d       Distribution
		wantErr bool
	}{
		{name: "constant", d: Constant(3)},
		{name: "constant zero", d: Constant(0)},
		{name: "uniform", d: Uniform(1, 10)},
		{name: "uniform single point", d: Uniform(4, 4)},
		{name: "uniform reversed", d: Uniform(10, 1), wantErr: true},
		{name: "gaussian", d: Gaussian(10, 3)},
		{name: "gaussian zero sigma", d: Gaussian(10, 0)},
		{name: "gaussian negative sigma", d: Gaussian(10, -1), wantErr: true},
		{name: "no kind", d: Distribution{}, wantErr: true},
		{name: "unknown kind", d: Distribution{Kind: "poisson"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.d.Validate()
			if got, want := err != nil, tc.wantErr; got != want {
				t.Errorf("Validate() error = %v, want error %v", err, want)
			}
		})
	}
}

func TestConstantSample(t *testing.T) {
	r := testRand()
	d := Constant(7)
	for range 10 {
		if got, want := d.Sample(r), 7.0; got != want {
			t.Fatalf("Sample() = %v, want %v", got, want)
		}
		if got, want := d.SampleInt(r), 7; got != want {
			t.Fatalf("SampleInt() = %v, want %v", got, want)
		}
	}

	// Rounding to nearest is the doc's rule for turning a continuous parameter
	// into a count.
	if got, want := Constant(2.5).SampleInt(r), 3; got != want {
		t.Errorf("Constant(2.5).SampleInt() = %v, want %v", got, want)
	}
	if got, want := Constant(2.4).SampleInt(r), 2; got != want {
		t.Errorf("Constant(2.4).SampleInt() = %v, want %v", got, want)
	}
}

func TestUniformSampleInt(t *testing.T) {
	r := testRand()
	d := Uniform(1, 4)

	seen := map[int]int{}
	const draws = 4000
	for range draws {
		v := d.SampleInt(r)
		if v < 1 || v > 4 {
			t.Fatalf("SampleInt() = %v, want a value in [1,4]", v)
		}
		seen[v]++
	}

	// The endpoints are the point of sampling the integers directly: rounding a
	// continuous draw would give 1 and 4 half the weight of 2 and 3, and these
	// counts would come out near 500/1000/1000/500 rather than near 1000 each.
	for v := 1; v <= 4; v++ {
		if seen[v] < draws/8 {
			t.Errorf("SampleInt() drew %d only %d times in %d draws, want a roughly uniform share", v, seen[v], draws)
		}
	}
}

func TestUniformSampleFloat(t *testing.T) {
	r := testRand()
	d := Uniform(2, 5)
	for range 1000 {
		v := d.Sample(r)
		if v < 2 || v > 5 {
			t.Fatalf("Sample() = %v, want a value in [2,5]", v)
		}
	}
}

func TestUniformSingleValue(t *testing.T) {
	r := testRand()
	if got, want := Uniform(3, 3).SampleInt(r), 3; got != want {
		t.Errorf("Uniform(3,3).SampleInt() = %v, want %v", got, want)
	}
}

func TestGaussianSample(t *testing.T) {
	r := testRand()
	d := Gaussian(10, 2)

	const draws = 20000
	sum, sumSq := 0.0, 0.0
	for range draws {
		v := d.Sample(r)
		sum += v
		sumSq += v * v
	}
	mean := sum / draws
	variance := sumSq/draws - mean*mean

	if math.Abs(mean-10) > 0.1 {
		t.Errorf("Gaussian(10,2) mean = %v, want ~10", mean)
	}
	if math.Abs(math.Sqrt(variance)-2) > 0.1 {
		t.Errorf("Gaussian(10,2) stddev = %v, want ~2", math.Sqrt(variance))
	}
}

func TestSampleIntRange(t *testing.T) {
	r := testRand()

	for _, tc := range []struct {
		name   string
		d      Distribution
		lo, hi int
		want   int
	}{
		{name: "below floor", d: Constant(-5), lo: 1, hi: 10, want: 1},
		{name: "above ceiling", d: Constant(500), lo: 1, hi: 10, want: 10},
		{name: "within range", d: Constant(4), lo: 1, hi: 10, want: 4},
		// An empty supply -- a namespace with no pods to draw from -- has no
		// answer between lo and hi, and the floor is what the caller can act on.
		{name: "empty range", d: Constant(4), lo: 1, hi: 0, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.d.SampleIntRange(r, tc.lo, tc.hi); got != tc.want {
				t.Errorf("SampleIntRange(%d, %d) = %v, want %v", tc.lo, tc.hi, got, tc.want)
			}
		})
	}
}

func TestAllocate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		total   int
		weights []float64
		want    []int
	}{
		{name: "exact split", total: 100, weights: []float64{0.5, 0.3, 0.15, 0.05}, want: []int{50, 30, 15, 5}},
		// The largest-remainder method exists for this case: four independent
		// roundings of 2.5 would give 8 or 12 policies, not 10.
		{name: "ties", total: 10, weights: []float64{1, 1, 1, 1}, want: []int{3, 3, 2, 2}},
		{name: "unnormalized weights", total: 10, weights: []float64{2, 2, 1, 0}, want: []int{4, 4, 2, 0}},
		{name: "single bucket", total: 7, weights: []float64{0, 1, 0, 0}, want: []int{0, 7, 0, 0}},
		{name: "zero total", total: 0, weights: []float64{1, 1}, want: []int{0, 0}},
		{name: "negative weight ignored", total: 4, weights: []float64{-1, 1}, want: []int{0, 4}},
		// Nothing to distribute by means everything has to land somewhere: the
		// count of policies is a parameter, not a suggestion.
		{name: "no positive weight", total: 5, weights: []float64{0, 0}, want: []int{5, 0}},
		{name: "no buckets", total: 5, weights: nil, want: []int{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := allocate(tc.total, tc.weights)
			if !slices.Equal(got, tc.want) {
				t.Errorf("allocate(%d, %v) = %v, want %v", tc.total, tc.weights, got, tc.want)
			}
			sum := 0
			for _, c := range got {
				sum += c
			}
			if want := max(tc.total, 0); len(tc.weights) > 0 && sum != want {
				t.Errorf("allocate(%d, %v) sums to %d, want %d", tc.total, tc.weights, sum, want)
			}
		})
	}
}
