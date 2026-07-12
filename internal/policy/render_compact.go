package policy

import (
	"fmt"

	"k8s.io/apimachinery/pkg/types"

	"github.com/bowei/xnp/internal/compact"
	"github.com/bowei/xnp/internal/tuple"
)

// RenderCompact is RenderIndexed's template-deduplicated counterpart:
// instead of materializing every pod's tuples with its own address
// substituted in, it keeps each distinct (namespace, identity) group's
// resolved ingress/egress rule set as one compact.Template, content-
// addressed by compact.TemplateID, and stores each pod as a small reference
// into that dictionary plus its own IPs, chunked by node — see
// internal/compact's package doc for why that turns output size from
// O(pods x rules) into O(templates + pods).
//
// Templates are deduplicated by content, not just by (namespace, identity):
// two unrelated groups that happen to resolve to byte-identical rule chains
// collapse to the same TemplateID, one dictionary entry smaller than
// groupTemplatesFor's own per-group cache achieves on its own.
//
// Output, after expanding every PodEntry against its Template via
// compact.Template.Expand, is the same tuple set RenderIndexed produces
// (see the differential test in render_compact_test.go).
func RenderCompact(in *Input) (*compact.Output, error) {
	st, err := prepareIndexedRender(in)
	if err != nil {
		return nil, err
	}

	templates := map[compact.TemplateID]compact.Template{}
	groupID := map[groupKey]compact.TemplateID{}
	nodeChunks := map[string]*compact.NodeChunk{}
	var nodeOrder []string

	for i, pod := range st.sortedPods {
		key := groupKey{pod.Namespace, st.podIdentity[i]}
		id, ok := groupID[key]
		if !ok {
			tmpl, err := st.groupTemplatesFor(key)
			if err != nil {
				return nil, fmt.Errorf("pod %s/%s: %w", in.Table.String(pod.Namespace), pod.Name, err)
			}
			ct, err := toCompactTemplate(tmpl)
			if err != nil {
				return nil, fmt.Errorf("pod %s/%s: %w", in.Table.String(pod.Namespace), pod.Name, err)
			}
			id = ct.ID()
			templates[id] = ct
			groupID[key] = id
		}

		node := pod.Original.Spec.NodeName
		chunk, ok := nodeChunks[node]
		if !ok {
			chunk = &compact.NodeChunk{Node: node}
			nodeChunks[node] = chunk
			nodeOrder = append(nodeOrder, node)
		}
		chunk.Pods = append(chunk.Pods, compact.PodEntry{
			Pod:      types.NamespacedName{Namespace: in.Table.String(pod.Namespace), Name: pod.Name},
			IPs:      podIPStrings(pod),
			Template: id,
		})
	}

	out := &compact.Output{Templates: templates, Nodes: make([]compact.NodeChunk, 0, len(nodeOrder))}
	for _, node := range nodeOrder {
		out.Nodes = append(out.Nodes, *nodeChunks[node])
	}
	return out, nil
}

// toCompactTemplate converts a resolved groupTemplates into its wire form.
func toCompactTemplate(tmpl *groupTemplates) (compact.Template, error) {
	ingress, err := toCompactEntries(tmpl.ingress)
	if err != nil {
		return compact.Template{}, err
	}
	egress, err := toCompactEntries(tmpl.egress)
	if err != nil {
		return compact.Template{}, err
	}
	return compact.Template{Ingress: ingress, Egress: egress}, nil
}

func toCompactEntries(tts []tupleTemplate) ([]compact.Entry, error) {
	entries := make([]compact.Entry, len(tts))
	for i, tt := range tts {
		e := compact.Entry{
			Verdict:      tt.verdict,
			PeerWildcard: tt.peer.wildcard,
			PeerAddr:     tt.peer.addr,
			PortWildcard: tt.port.wildcard,
		}
		if !tt.port.wildcard {
			protocol, err := tuple.ProtocolFromK8s(tt.port.protocol)
			if err != nil {
				return nil, err
			}
			e.Protocol = protocol
			if tt.port.port != nil {
				e.Port = *tt.port.port
				e.HasPort = true
			}
			if tt.port.endPort != nil {
				e.EndPort = *tt.port.endPort
				e.HasEndPort = true
			}
		}
		entries[i] = e
	}
	return entries, nil
}
