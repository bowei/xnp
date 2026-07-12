// Package compact defines a template-deduplicated wire format for a
// rendered policy.Output, chunked by node so a lightweight per-node agent
// can fetch only its own chunk and apply incremental updates to it.
//
// The insight it exploits: policy.RenderIndexed already discovers that most
// pods share an identical ingress/egress rule Template — that's the entire
// reason it groups pods by (namespace, identity) before resolving selectors.
// The flat tuple output throws that sharing away by fully materializing
// every pod's tuples with its own address substituted in. Output instead
// keeps templates deduplicated (content-addressed by TemplateID, so byte-
// identical templates from unrelated groups collapse too) and stores each
// pod as a small reference into the template dictionary plus its own IPs,
// turning wire size from O(pods x rules) into O(templates + pods).
//
// This package intentionally has no dependency on internal/policy or
// internal/repr — it's meant to be linkable by the node agent on its own,
// without pulling in the selector-matching/rendering machinery that only
// the control plane needs.
package compact

import (
	"crypto/sha256"
	"encoding/binary"
	"io"

	"k8s.io/apimachinery/pkg/types"

	"github.com/bowei/xnp/internal/tuple"
)

// Entry is one resolved rule within a Template: a peer/port/verdict triple,
// equivalent to one iteration of policy.writeTuples' inner loop but with the
// pod's own address left out, since that's supplied per-pod at Expand time.
type Entry struct {
	Verdict tuple.Verdict

	// PeerWildcard means "any peer"; PeerAddr (a concrete IP or CIDR) is
	// only meaningful when it's false.
	PeerWildcard bool
	PeerAddr     string

	// PortWildcard means "any port/protocol"; Protocol/Port/EndPort are
	// only meaningful when it's false. Port/EndPort further need
	// HasPort/HasEndPort since "any port of this protocol" is valid too.
	PortWildcard bool
	Protocol     tuple.Protocol
	Port         int32
	HasPort      bool
	EndPort      int32
	HasEndPort   bool
}

// Template is the deduplicated ingress/egress rule chain shared by every pod
// with the same TemplateID. Entry order is significant — see tuple.Tuple's
// doc comment on first-match-wins chains — so TemplateID must be (and is)
// computed order-sensitively.
type Template struct {
	Ingress []Entry
	Egress  []Entry
}

// TemplateID content-addresses a Template: two Templates with identical
// entries in identical order hash to the same ID even if they were computed
// for unrelated (namespace, identity) groups, letting node agents cache
// dictionary entries by ID across renders where the ID didn't change.
type TemplateID [16]byte

// ID computes t's TemplateID from its contents.
func (t Template) ID() TemplateID {
	h := sha256.New()
	writeEntries(h, t.Ingress)
	writeEntries(h, t.Egress)
	var id TemplateID
	copy(id[:], h.Sum(nil))
	return id
}

// writeEntries feeds entries into h in a fixed-width, order-sensitive
// encoding, so Template.ID is a pure function of Ingress/Egress content.
// hash.Hash.Write never returns an error, so its result is discarded.
func writeEntries(h io.Writer, entries []Entry) {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(entries)))
	_, _ = h.Write(lenBuf[:])

	var buf [14]byte
	for _, e := range entries {
		buf[0] = byte(e.Verdict)
		buf[1] = boolByte(e.PeerWildcard)
		buf[2] = boolByte(e.PortWildcard)
		buf[3] = byte(e.Protocol)
		buf[4] = boolByte(e.HasPort)
		buf[5] = boolByte(e.HasEndPort)
		binary.BigEndian.PutUint32(buf[6:10], uint32(e.Port))
		binary.BigEndian.PutUint32(buf[10:14], uint32(e.EndPort))
		_, _ = h.Write(buf[:])

		var addrLen [4]byte
		binary.BigEndian.PutUint32(addrLen[:], uint32(len(e.PeerAddr)))
		_, _ = h.Write(addrLen[:])
		_, _ = io.WriteString(h, e.PeerAddr)
	}
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// PodEntry is one pod's reference into the template dictionary: its own
// IPs (substituted into its Template's wildcarded "own address" side at
// Expand time) plus the ID of the shared Template it resolved to.
type PodEntry struct {
	Pod      types.NamespacedName
	IPs      []string
	Template TemplateID
}

// NodeChunk is every PodEntry for pods scheduled to one node — the unit a
// node agent fetches and applies incremental updates to.
type NodeChunk struct {
	Node string
	Pods []PodEntry
}

// Output is a full compact render: the template dictionary plus every
// node's chunk.
type Output struct {
	Templates map[TemplateID]Template
	Nodes     []NodeChunk
}

// EstimatedSize returns a rough byte count for a straightforward fixed-width
// wire encoding of o's template dictionary and pod table, kept separate
// since a real transport would ship the dictionary once and let node agents
// that already have a given TemplateID cached skip re-fetching it on later,
// incremental updates — see the package doc. This is an estimate for sizing
// discussions, not a guarantee about any actual serializer's output.
func (o *Output) EstimatedSize() (templateBytes, podTableBytes int) {
	for _, t := range o.Templates {
		templateBytes += t.estimatedSize()
	}
	for _, chunk := range o.Nodes {
		for _, pe := range chunk.Pods {
			podTableBytes += pe.estimatedSize()
		}
	}
	return templateBytes, podTableBytes
}

// TupleCount returns the number of concrete Tuples o's PodEntries expand to
// in total — the size of the equivalent flat (uncompressed) output, for
// comparison against EstimatedSize.
func (o *Output) TupleCount() int {
	n := 0
	for _, chunk := range o.Nodes {
		for _, pe := range chunk.Pods {
			tmpl := o.Templates[pe.Template]
			ips := len(pe.IPs)
			if ips == 0 {
				ips = 1
			}
			n += (len(tmpl.Ingress) + len(tmpl.Egress)) * ips
		}
	}
	return n
}

func (t Template) estimatedSize() int {
	n := len(TemplateID{}) // dictionary key
	for _, e := range t.Ingress {
		n += e.estimatedSize()
	}
	for _, e := range t.Egress {
		n += e.estimatedSize()
	}
	return n
}

// estimatedSize is Entry's fixed fields (verdict, 3 flag bytes, protocol,
// port, endPort) plus a length-prefixed PeerAddr.
func (e Entry) estimatedSize() int {
	const fixed = 1 + 3 + 1 + 4 + 4
	return fixed + 4 + len(e.PeerAddr)
}

// estimatedSize is a TemplateID reference plus length-prefixed pod name and
// IP strings.
func (pe PodEntry) estimatedSize() int {
	n := len(TemplateID{})
	n += 4 + len(pe.Pod.Namespace) + 4 + len(pe.Pod.Name)
	for _, ip := range pe.IPs {
		n += 4 + len(ip)
	}
	return n
}

// Expand reconstitutes the concrete Tuples pod's own IPs and t together
// imply, in the same Src/Dest/own-address-substitution order
// policy.writeTuples produces them in — the operation a node agent performs
// once per pod after fetching its NodeChunk and resolving each PodEntry's
// TemplateID against the dictionary.
func (t Template) Expand(pod types.NamespacedName, ips []string) []tuple.Tuple {
	if len(ips) == 0 {
		ips = []string{""}
	}
	out := make([]tuple.Tuple, 0, (len(t.Ingress)+len(t.Egress))*len(ips))
	out = expandDirection(out, pod, ips, tuple.DirectionIngress, t.Ingress)
	out = expandDirection(out, pod, ips, tuple.DirectionEgress, t.Egress)
	return out
}

func expandDirection(dst []tuple.Tuple, pod types.NamespacedName, ips []string, dir tuple.Direction, entries []Entry) []tuple.Tuple {
	for _, e := range entries {
		peerAddr := ""
		if !e.PeerWildcard {
			peerAddr = e.PeerAddr
		}

		var protocol tuple.Protocol
		var portPtr, endPortPtr *int32
		if !e.PortWildcard {
			protocol = e.Protocol
			if e.HasPort {
				p := e.Port
				portPtr = &p
			}
			if e.HasEndPort {
				p := e.EndPort
				endPortPtr = &p
			}
		}

		for _, own := range ips {
			row := tuple.Tuple{
				Pod:       pod,
				Port:      portPtr,
				EndPort:   endPortPtr,
				Protocol:  protocol,
				Direction: dir,
				Verdict:   e.Verdict,
			}
			if dir == tuple.DirectionIngress {
				row.Src, row.Dest = peerAddr, own
			} else {
				row.Src, row.Dest = own, peerAddr
			}
			dst = append(dst, row)
		}
	}
	return dst
}
