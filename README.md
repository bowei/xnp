# README

xnp is an implementation of Kubernetes Network Policy that is targeted for high scale.

The key architectural design in `xnp` is to prebake as much of resolution of policies
in a central horizontally-scalable control plane that then sends a highly compressed list of simple rules to be consumed by a light-weight agent that operates on simple network filtering rules on each individual Kubernetes Node.

## Network policy computation

Describe  Kubernetes network policy computation as an algorithm to take the set
of policies and compile them down to a set of tuples:

  (pod, src, dest, port, protocol, direction, verdict)

where:

* pod: namespace, name
* src, dest: are IP (v4 or v6) addresses
* port: is optional TCP or UDP port
* direction: egress or ingress
* verdict: allow or deny

## Scale testing

internal/scaletest will contain scale testing related utilities.

* internal/scaletest/testcase.go will generate large sets of network 
  policies and pods across many namespaces.
* hack/profile.sh will run a profiling of the rendering.

## command line

Use golang cobra for command parsing.

```
# Run centralized policy renderer.
$ xnp server

# Run client that will receive the policies from the server.
$ xnp client
```