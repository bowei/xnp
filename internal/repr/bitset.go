package repr

// Bitset is a fixed-size bit vector, used to store per-identity match
// results so membership tests downstream are a single word AND instead of a
// set/map lookup.
type Bitset []uint64

func NewBitset(n int) Bitset {
	return make(Bitset, (n+63)/64)
}

func (b Bitset) Set(i uint32) {
	b[i/64] |= 1 << (i % 64)
}

func (b Bitset) Test(i uint32) bool {
	if int(i/64) >= len(b) {
		return false
	}
	return b[i/64]&(1<<(i%64)) != 0
}
