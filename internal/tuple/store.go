package tuple

// Store holds a rendered Tuple set.
type Store interface {
	Reserve(n int) []Tuple
	AddAll(ts []Tuple)
	Len() int
	List() []Tuple
}
