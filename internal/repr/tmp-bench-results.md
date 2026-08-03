Results (M3, Go 1.26)
Total wall time for the whole batch, and bytes the batch leaves resident:

1k	10k	100k	1M	per obj
Namespace minimal	0.14 ms	1.4 ms	15 ms	154 ms	144 B
medium	0.45 ms	4.6 ms	44 ms	461 ms	416 B
large	2.4 ms	24 ms	334 ms	2.55 s	1.5 KB
Pod minimal	0.17 ms	1.9 ms	28 ms	285 ms	168 B
medium	0.50 ms	5.1 ms	58 ms	607 ms	464 B
large	2.4 ms	25 ms	258 ms	2.65 s	1.5 KB
NetworkPolicy minimal	0.99 ms	9.4 ms	97 ms	1.08 s	2.1 KB
medium	12.8 ms	129 ms	1.35 s	skipped: ~7.6 GiB	26 KB
large	143 ms	1.51 s	skipped: ~7.4 GiB	skipped: ~74 GiB	221 KB
Shapes: minimal = 1 label, 1-rule/1-peer/1-port policy. medium = 8 labels, dual-stack, 3+3 rules × 3 peers × 3 ports. large = 32 labels, dual-stack, 10+10 rules × 8 peers × 8 ports.

What it says: pods and namespaces are cheap — a 1M-pod resync converts in 0.3–2.7 s and costs 168 B–1.5 KB each, so repr is not the bottleneck there. Policies are 3–4 orders of magnitude more expensive per object, and the binding constraint is memory, not time: a large policy is ~220 KB in repr form because each of its 160 peers compiles up to two pred.Predicates. 100k large policies won't fit in RAM at all. ns/obj is nearly flat across N for pods/namespaces (GC keeps up), which is the useful negative result.

Two deliberate bounds, both documented in the file header:

Source objects come from a pool capped at 256 MB and cycled — holding 1M large corev1.Pods costs GBs before any conversion. Converted objects are all retained, since that's what repr is for.
A case whose result exceeds -repr.membudget (default 4 GiB) skips itself and prints the estimate rather than swapping the machine. Those skips are silent without -v — and for the large policy shapes the estimate is the answer.