package managedrule

import (
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

// jmapID is the wire form of a JMAP id (RFC 8620 §1.2).
type jmapID = string

// jmapCondition is the wire-form condition object.
type jmapCondition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// jmapAction is the wire-form action object.
type jmapAction struct {
	Kind   string         `json:"kind"`
	Params map[string]any `json:"params,omitempty"`
}

// jmapManagedRule is the wire-form ManagedRule object.
type jmapManagedRule struct {
	ID         jmapID          `json:"id"`
	Name       string          `json:"name"`
	Enabled    bool            `json:"enabled"`
	Order      int             `json:"order"`
	Conditions []jmapCondition `json:"conditions"`
	Actions    []jmapAction    `json:"actions"`
}

// setError is the per-create/update/destroy error object per RFC 8620 §5.3.
type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

// idForRule renders a store ManagedRuleID as the JMAP wire id.
func idForRule(id store.ManagedRuleID) jmapID {
	return strconv.FormatInt(int64(id), 10)
}

// ruleFromID parses a JMAP wire id back to a store.ManagedRuleID.
// Returns (0, false) when the id is non-numeric or zero.
func ruleFromID(id jmapID) (store.ManagedRuleID, bool) {
	v, err := strconv.ParseInt(id, 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return store.ManagedRuleID(v), true
}

// ruleToWire converts a store.ManagedRule to the wire shape.
func ruleToWire(r store.ManagedRule) jmapManagedRule {
	conds := make([]jmapCondition, len(r.Conditions))
	for i, c := range r.Conditions {
		conds[i] = jmapCondition{Field: c.Field, Op: c.Op, Value: c.Value}
	}
	acts := make([]jmapAction, len(r.Actions))
	for i, a := range r.Actions {
		acts[i] = jmapAction{Kind: a.Kind, Params: a.Params}
	}
	return jmapManagedRule{
		ID:         idForRule(r.ID),
		Name:       r.Name,
		Enabled:    r.Enabled,
		Order:      r.SortOrder,
		Conditions: conds,
		Actions:    acts,
	}
}

// ruleFromWire converts wire conditions+actions back to store types.
func ruleFromWire(w jmapManagedRule) store.ManagedRule {
	conds := make([]store.RuleCondition, len(w.Conditions))
	for i, c := range w.Conditions {
		conds[i] = store.RuleCondition{Field: c.Field, Op: c.Op, Value: c.Value}
	}
	acts := make([]store.RuleAction, len(w.Actions))
	for i, a := range w.Actions {
		acts[i] = store.RuleAction{Kind: a.Kind, Params: a.Params}
	}
	return store.ManagedRule{
		Name:       w.Name,
		Enabled:    w.Enabled,
		SortOrder:  w.Order,
		Conditions: conds,
		Actions:    acts,
	}
}

// stateString converts a store JMAP counter to the wire opaque string.
func stateString(n int64) string { return strconv.FormatInt(n, 10) }
