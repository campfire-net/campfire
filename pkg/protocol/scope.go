package protocol

// scope.go — ScopeEnforcer checks campfire and operation-class access against
// a ScopeConfig. Pure data structure; not wired into Client (follow-on item).
//
// Operation class mapping:
//
//	read     — Read, Await, Subscribe, Members
//	write    — Send
//	admin    — Join, Leave, Create, Disband, Admit, Evict, AddPeer, RemovePeer
//	identity — cf home be / cf home be --revoke

import "fmt"

// ScopeEnforcer checks operations against a ScopeConfig.
type ScopeEnforcer struct {
	cfg ScopeConfig
}

// NewScopeEnforcer returns a ScopeEnforcer for the given ScopeConfig.
func NewScopeEnforcer(cfg ScopeConfig) *ScopeEnforcer {
	return &ScopeEnforcer{cfg: cfg}
}

// CheckCampfire returns an error if campfireID is not in the allowlist.
// Returns nil if the allowlist is empty (unrestricted).
func (e *ScopeEnforcer) CheckCampfire(campfireID string) error {
	if len(e.cfg.Campfires) == 0 {
		return nil
	}
	for _, id := range e.cfg.Campfires {
		if id == campfireID {
			return nil
		}
	}
	return fmt.Errorf("scope: campfire %q is not in the allowlist", campfireID)
}

// CheckOperation returns an error if opClass is not in the allowed classes.
// Returns nil if operation_classes is empty (unrestricted).
func (e *ScopeEnforcer) CheckOperation(opClass string) error {
	if len(e.cfg.OperationClasses) == 0 {
		return nil
	}
	for _, class := range e.cfg.OperationClasses {
		if class == opClass {
			return nil
		}
	}
	return fmt.Errorf("scope: operation class %q is not permitted", opClass)
}
