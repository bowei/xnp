// Package repr holds memory-efficient mirrors of the Kubernetes types
// internal/policy renders (Pod, NetworkPolicy, Namespace): IPs as
// netip.Addr/netip.Prefix instead of strings, and labels as an interned,
// pre-sorted form instead of map[string]string. This is storage-layer work
// only — see lookup.go for the matching-layer structures (compiled
// selectors, inverted index, bitsets) built on top of it.
package repr

import (
	"fmt"
	"net/netip"
	"slices"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

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

// kvPair packs a label key/value StringID pair into a single comparable,
// hashable, sortable value (key in the high bits so sorting by kvPair also
// sorts by key first). Used both as LabelSet's canonical form and as
// CompiledSelector's exact-match requirements (see lookup.go), so the two
// sides of a match can compare pairs directly instead of unpacking fields.
type kvPair uint64

func newKVPair(key, value StringID) kvPair {
	return kvPair(key)<<32 | kvPair(value)
}

func (p kvPair) key() StringID   { return StringID(p >> 32) }
func (p kvPair) value() StringID { return StringID(uint32(p)) }

// LabelSet is a pod's or namespace's labels canonicalized into interned
// integer form: kv gives O(1) key lookup, sorted gives a canonical,
// hashable, iterable form for later identity/index work (see lookup.go).
type LabelSet struct {
	kv     map[StringID]StringID
	sorted []kvPair
}

// NewLabelSet interns labels' keys/values via tbl and returns their
// canonical LabelSet form.
func NewLabelSet(labels map[string]string, tbl *SymbolTable) LabelSet {
	kv := make(map[StringID]StringID, len(labels))
	sorted := make([]kvPair, 0, len(labels))
	for k, v := range labels {
		key, val := tbl.Intern(k), tbl.Intern(v)
		kv[key] = val
		sorted = append(sorted, newKVPair(key, val))
	}
	slices.Sort(sorted)
	return LabelSet{kv: kv, sorted: sorted}
}

// Get returns the interned value StringID for key, if present.
func (ls LabelSet) Get(key StringID) (StringID, bool) {
	v, ok := ls.kv[key]
	return v, ok
}

// Len is the number of labels in the set.
func (ls LabelSet) Len() int {
	return len(ls.sorted)
}

// Protocol is corev1.Protocol packed into a byte instead of a string; the
// NetworkPolicy API only ever produces one of the three values below.
type Protocol uint8

const (
	ProtocolTCP Protocol = iota
	ProtocolUDP
	ProtocolSCTP
)

func protocolFromK8s(p corev1.Protocol) (Protocol, error) {
	switch p {
	case corev1.ProtocolTCP:
		return ProtocolTCP, nil
	case corev1.ProtocolUDP:
		return ProtocolUDP, nil
	case corev1.ProtocolSCTP:
		return ProtocolSCTP, nil
	default:
		return 0, fmt.Errorf("unsupported protocol %q", p)
	}
}

// IPBlock is the memory-efficient mirror of a networkingv1.IPBlock.
//
// Except is stored for completeness but, matching internal/policy's
// documented simplification, is not carved out of CIDR by anything that
// consumes repr yet.
type IPBlock struct {
	CIDR   netip.Prefix
	Except []netip.Prefix
}

func newIPBlock(b *networkingv1.IPBlock) (*IPBlock, error) {
	if b == nil {
		return nil, nil
	}
	cidr, err := netip.ParsePrefix(b.CIDR)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", b.CIDR, err)
	}
	var except []netip.Prefix
	for _, e := range b.Except {
		p, err := netip.ParsePrefix(e)
		if err != nil {
			return nil, fmt.Errorf("invalid except CIDR %q: %w", e, err)
		}
		except = append(except, p)
	}
	return &IPBlock{CIDR: cidr, Except: except}, nil
}
