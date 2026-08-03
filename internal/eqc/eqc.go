package eqc

import "github.com/bowei/kvec/pkg/kv"

// Classes maintains equivalence classes of labeled objects, grouped by
// namespace. Objects in the same namespace carrying equal key/value sets share
// a single Class, so anything that depends only on an object's labels — most
// importantly whether a selector matches it — can be computed once per Class
// instead of once per object.
//
// The zero value is empty and ready to use. Classes is not safe for concurrent
// use.
type Classes struct {
	// byNs maps namespace to perNs information.
	byNs map[string]*perNs
}

type perNs struct {
	// partitions map from KV.Hash() to EqClass for that hash. This should
	// almost always be len == 1 (no collison).
	partitions map[uint64][]*Class

	// byName is the reverse index from object name to the Class currently
	// holding it. Without it, every Get/Update/Delete would have to scan the
	// namespace's partitions to find the one object it is after.
	byName map[string]*Class
}

// Class of objects that share the same KV labels.
type Class struct {
	kv *kv.KV
	ns string

	// objs associated with this EqClass. key is the name
	objs map[string]struct{}
}

// KV is the key/value set shared by every object in the Class. It must not be
// mutated.
func (c *Class) KV() *kv.KV { return c.kv }

// Namespace the Class belongs to.
func (c *Class) Namespace() string { return c.ns }

// Len is the number of objects currently in the Class.
func (c *Class) Len() int { return len(c.objs) }

// Has returns whether the named object is in the Class.
func (c *Class) Has(name string) bool {
	_, ok := c.objs[name]
	return ok
}

// Objects returns an iterator over the names of the objects in the Class. The
// order is unspecified. The Class must not be modified during iteration.
func (c *Class) Objects() func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for name := range c.objs {
			if !yield(name) {
				return
			}
		}
	}
}

// Get returns the Class holding the named object, and whether the object is
// tracked at all.
func (cl *Classes) Get(ns, name string) (*Class, bool) {
	pns, ok := cl.byNs[ns]
	if !ok {
		return nil, false
	}
	cls, ok := pns.byName[name]
	return cls, ok
}

// Add the named object with the given key/value set, creating the equivalence
// class for kv if this is the first object to carry it. Adding a name that is
// already tracked in ns behaves as an Update of that object.
func (cl *Classes) Add(ns, name string, kv *kv.KV) {
	cl.set(ns, name, kv)
}

// Update the key/value set of the named object, moving it to the class for kv
// (creating that class if needed) and dropping the class it came from if that
// leaves it empty. An object that is not currently tracked is added, so a
// missed Add does not lose the object.
func (cl *Classes) Update(ns, name string, kv *kv.KV) {
	cl.set(ns, name, kv)
}

// set places the named object in the class for kv, evicting it from whatever
// class it is in now. Add and Update are the same operation approached from
// either side: both have to handle the object being absent (nothing to evict)
// and the object already sitting in the right class (nothing to do).
func (cl *Classes) set(ns, name string, kv *kv.KV) {
	pns, ok := cl.byNs[ns]
	if !ok {
		pns = &perNs{
			partitions: map[uint64][]*Class{},
			byName:     map[string]*Class{},
		}
		if cl.byNs == nil {
			cl.byNs = map[string]*perNs{}
		}
		cl.byNs[ns] = pns
	}

	if cur, ok := pns.byName[name]; ok {
		if cur.kv.Equal(kv) {
			// Already in the right class: the common Update case, where an
			// object changed in some way that did not touch its labels.
			return
		}
		pns.evict(name, cur)
	}

	cls := pns.classFor(ns, kv)
	cls.objs[name] = struct{}{}
	pns.byName[name] = cls
}

// Delete the named object. Deleting an object that is not tracked is a no-op.
func (cl *Classes) Delete(ns, name string) {
	pns, ok := cl.byNs[ns]
	if !ok {
		return
	}
	cls, ok := pns.byName[name]
	if !ok {
		return
	}
	pns.evict(name, cls)
	if len(pns.partitions) == 0 {
		// Namespaces are pruned as they empty so that iteration cost tracks
		// the live objects rather than every namespace ever seen.
		delete(cl.byNs, ns)
	}
}

// Range returns an iterator over the Classes in ns. The order is unspecified.
// The Classes must not be modified during iteration.
func (cl *Classes) Range(ns string) func(yield func(*Class) bool) {
	return func(yield func(*Class) bool) {
		pns, ok := cl.byNs[ns]
		if !ok {
			return
		}
		for _, bucket := range pns.partitions {
			for _, cls := range bucket {
				if !yield(cls) {
					return
				}
			}
		}
	}
}

// All returns an iterator over every Class in every namespace. The order is
// unspecified; a Class carries its own Namespace(). The Classes must not be
// modified during iteration.
func (cl *Classes) All() func(yield func(*Class) bool) {
	return func(yield func(*Class) bool) {
		for _, pns := range cl.byNs {
			for _, bucket := range pns.partitions {
				for _, cls := range bucket {
					if !yield(cls) {
						return
					}
				}
			}
		}
	}
}

// classFor returns the Class in this namespace holding labels, creating it if
// no object has carried that exact set yet. Hash alone does not establish
// identity — 64 bits cannot separate every distinct content — so the bucket it
// selects is walked with Equal to settle the rare collision.
func (pns *perNs) classFor(ns string, labels *kv.KV) *Class {
	hash := labels.Hash()
	for _, cls := range pns.partitions[hash] {
		if cls.kv.Equal(labels) {
			return cls
		}
	}
	cls := &Class{
		kv:   labels,
		ns:   ns,
		objs: map[string]struct{}{},
	}
	pns.partitions[hash] = append(pns.partitions[hash], cls)
	return cls
}

// evict removes the named object from cls, dropping cls once it holds no
// objects. Emptied classes cannot be left in place: they would keep the
// namespace alive and make Range hand out classes with nothing in them.
func (pns *perNs) evict(name string, cls *Class) {
	delete(cls.objs, name)
	delete(pns.byName, name)
	if len(cls.objs) > 0 {
		return
	}

	hash := cls.kv.Hash()
	bucket := pns.partitions[hash]
	for i, other := range bucket {
		if other != cls {
			continue
		}
		// Order within a bucket carries no meaning, so the last entry can fill
		// the hole rather than shifting the rest down.
		bucket[i] = bucket[len(bucket)-1]
		bucket = bucket[:len(bucket)-1]
		break
	}
	if len(bucket) == 0 {
		delete(pns.partitions, hash)
	} else {
		pns.partitions[hash] = bucket
	}
}
