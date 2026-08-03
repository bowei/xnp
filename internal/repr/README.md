# README

This package implements a parallel data structures to the K8s API types that
contain optimized data structures needed to resolve network policies.

* Contains only the fields that are needed.
* Converts labels and selectors to precompued KV and Predicates that are more
  efficient for matching.
* Keeps no reference to the API object it was converted from, so that object
  can be dropped once the mirror exists.

A Pod's labels are the exception: they stay a plain map, and the KV is built by
`RelevantLabels` against the label keys the cluster's policies actually select
on. That key set is not known at conversion time, and it changes as policies
come and go, so the projection has to be redone outside conversion.