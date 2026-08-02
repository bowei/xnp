# README

This package implements a parallel data structures to the K8s API types that
contain optimized data structures needed to resolve network policies.

* Contains only the fields that are needed.
* Converts labels and selectors to precompued KV and Predicates that are more
  efficient for matching.