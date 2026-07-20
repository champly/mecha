package core

import "sync"

// registry maintains thread-safe id/role indexes of agentd instances.
type registry struct {
	mu     sync.Mutex
	byID   map[string]*instance
	byRole map[string]*instance
}

func newRegistry() *registry {
	return &registry{
		byID:   make(map[string]*instance),
		byRole: make(map[string]*instance),
	}
}

func (r *registry) add(inst *instance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[inst.id] = inst
	r.byRole[inst.role] = inst
}

func (r *registry) get(id string) *instance {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[id]
}

func (r *registry) getByRole(role string) *instance {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byRole[role]
}

// remove deletes only index entries that still point to inst, so a respawned
// same-role instance is never removed by a stale destroy.
func (r *registry) remove(inst *instance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID[inst.id] == inst {
		delete(r.byID, inst.id)
	}
	if r.byRole[inst.role] == inst {
		delete(r.byRole, inst.role)
	}
}

func (r *registry) all() []*instance {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*instance, 0, len(r.byID))
	for _, inst := range r.byID {
		out = append(out, inst)
	}
	return out
}
