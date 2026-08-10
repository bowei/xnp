package main

import (
	"fmt"
	"math"
	"math/rand/v2"
)

// DistKind names the shape of a Distribution.
type DistKind string

const (
	DistConstant DistKind = "constant"
	DistUniform  DistKind = "uniform"
	DistGaussian DistKind = "gaussian"
)

// Distribution is a randomized generation parameter. Every count the generator
// draws -- pods per namespace, the degree of a policy -- is one of these, so
// that a single set of parameters describes a family of graphs rather than one
// graph, which is the point of the exercise: the variants are what get
// compared.
//
// The three shapes share one struct rather than being an interface with three
// implementations, because these are read from and written back to a YAML file
// a person edits, and a tagged struct is the shape that survives that round
// trip without custom marshalling.
type Distribution struct {
	Kind DistKind `json:"kind"`

	// Value is the constant, for Kind == constant.
	Value float64 `json:"value,omitempty"`

	// Start and End bound a uniform distribution, inclusive on both ends.
	Start float64 `json:"start,omitempty"`
	End   float64 `json:"end,omitempty"`

	// Mu and Sigma parameterize a Gaussian.
	Mu    float64 `json:"mu,omitempty"`
	Sigma float64 `json:"sigma,omitempty"`
}

// Constant returns a distribution that always yields v.
func Constant(v float64) Distribution {
	return Distribution{Kind: DistConstant, Value: v}
}

// Uniform returns a distribution over [start, end], inclusive.
func Uniform(start, end float64) Distribution {
	return Distribution{Kind: DistUniform, Start: start, End: end}
}

// Gaussian returns a normal distribution with mean mu and standard deviation
// sigma.
func Gaussian(mu, sigma float64) Distribution {
	return Distribution{Kind: DistGaussian, Mu: mu, Sigma: sigma}
}

// Validate reports whether d is a distribution that can be sampled.
func (d Distribution) Validate() error {
	switch d.Kind {
	case DistConstant:
	case DistUniform:
		if d.End < d.Start {
			return fmt.Errorf("uniform distribution has end %v < start %v", d.End, d.Start)
		}
	case DistGaussian:
		if d.Sigma < 0 {
			return fmt.Errorf("gaussian distribution has negative sigma %v", d.Sigma)
		}
	case "":
		return fmt.Errorf("distribution has no kind")
	default:
		return fmt.Errorf("unknown distribution kind %q", d.Kind)
	}
	return nil
}

// Sample draws a value.
func (d Distribution) Sample(r *rand.Rand) float64 {
	switch d.Kind {
	case DistUniform:
		return d.Start + r.Float64()*(d.End-d.Start)
	case DistGaussian:
		return d.Mu + r.NormFloat64()*d.Sigma
	default:
		return d.Value
	}
}

// SampleInt draws an integer.
//
// A uniform distribution is sampled over the integers in [start, end] directly
// rather than by rounding a continuous draw, which would give the two endpoints
// half the weight of every other value -- these parameters are small counts
// ("between 1 and 4 pods"), so a skew at the ends is a skew over a quarter of
// the range. The other two kinds round to nearest, as the doc specifies.
func (d Distribution) SampleInt(r *rand.Rand) int {
	if d.Kind == DistUniform {
		lo, hi := int(math.Round(d.Start)), int(math.Round(d.End))
		if hi <= lo {
			return lo
		}
		return lo + r.IntN(hi-lo+1)
	}
	return int(math.Round(d.Sample(r)))
}

// SampleIntRange draws an integer and clamps it to [lo, hi], for the many
// parameters that are a count of something the graph has a finite supply of: a
// policy cannot select more pods than its namespace holds however large a
// degree the distribution suggests, and clamping is the only answer that keeps
// the rest of the parameters meaningful.
//
// A hi below lo means there is nothing to draw from and yields lo, leaving the
// caller to decide whether that is empty or a floor.
func (d Distribution) SampleIntRange(r *rand.Rand, lo, hi int) int {
	v := d.SampleInt(r)
	if v < lo {
		return lo
	}
	if v > hi {
		if hi < lo {
			return lo
		}
		return hi
	}
	return v
}

// allocate splits total into len(weights) counts in proportion to weights,
// using the largest-remainder method so that the counts sum to exactly total.
//
// Rounding each share independently would lose or gain policies -- a mix of
// four types each at 0.125 of 10 policies rounds to 4 policies, not 10 -- and
// the count of policies is a parameter the user set, not one to be approximated.
// Negative weights are treated as zero; all-zero weights put everything in the
// first bucket, which is the only allocation that respects the total.
func allocate(total int, weights []float64) []int {
	counts := make([]int, len(weights))
	if len(weights) == 0 || total <= 0 {
		return counts
	}

	sum := 0.0
	for _, w := range weights {
		if w > 0 {
			sum += w
		}
	}
	if sum == 0 {
		counts[0] = total
		return counts
	}

	assigned := 0
	remainders := make([]float64, len(weights))
	for i, w := range weights {
		if w <= 0 {
			continue
		}
		exact := float64(total) * w / sum
		counts[i] = int(exact)
		remainders[i] = exact - float64(counts[i])
		assigned += counts[i]
	}

	// Hand the leftovers to the largest remainders, ties going to the earlier
	// bucket so the result depends only on the weights and not on a sort's
	// stability.
	for ; assigned < total; assigned++ {
		best, bestRem := -1, -1.0
		for i, rem := range remainders {
			if weights[i] > 0 && rem > bestRem {
				best, bestRem = i, rem
			}
		}
		if best < 0 {
			counts[0] += total - assigned
			break
		}
		counts[best]++
		remainders[best] = -1
	}

	return counts
}
