// Package hosting provides per-operator usage metering for the campfire hosted
// service. It maps campfire IDs to operator account IDs and batches message
// counts into hourly UsageEvents sent to Forge's ingest endpoint.
package hosting

import "sync"

// OperatorMapper tracks which operator account owns which campfire.
// It is safe for concurrent use.
type OperatorMapper struct {
	mu sync.RWMutex
	m  map[string]string // campfireID → operatorAccountID
}

// NewOperatorMapper creates an empty OperatorMapper.
func NewOperatorMapper() *OperatorMapper {
	return &OperatorMapper{
		m: make(map[string]string),
	}
}

// Register records that campfireID belongs to operatorAccountID.
// Overwrites any previous mapping for the same campfire.
func (om *OperatorMapper) Register(campfireID, operatorAccountID string) {
	om.mu.Lock()
	defer om.mu.Unlock()
	om.m[campfireID] = operatorAccountID
}

// OperatorFor returns the operator account ID for campfireID, and whether it
// was found. Returns ("", false) if the campfire has not been registered.
func (om *OperatorMapper) OperatorFor(campfireID string) (string, bool) {
	om.mu.RLock()
	defer om.mu.RUnlock()
	id, ok := om.m[campfireID]
	return id, ok
}
