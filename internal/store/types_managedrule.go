package store

import "time"

// ManagedRuleID is the primary key of a managed_rules row.
type ManagedRuleID int64

// RuleCondition is one filter condition in a ManagedRule.
// Field is one of: "from", "to", "subject", "has-attachment",
// "thread-id", "from-domain".
// Op is one of: "contains", "equals", "wildcard-match".
// Value is the match target (ignored for "has-attachment").
type RuleCondition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// RuleAction is one action in a ManagedRule.
// Kind is one of: "apply-label", "skip-inbox", "mark-read", "delete",
// "forward".
// Params carries kind-specific parameters:
//   - "apply-label":  { "label": "<name>" }
//   - "forward":      { "to": "<address>" }
//   - others:         {} (no parameters)
type RuleAction struct {
	Kind   string         `json:"kind"`
	Params map[string]any `json:"params,omitempty"`
}

// ManagedRule is one row of the managed_rules table. Conditions are
// AND-combined; any enabled rule whose conditions all match triggers its
// actions in order.
type ManagedRule struct {
	// ID is the server-assigned primary key.
	ID ManagedRuleID
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// Name is an optional human-readable label (may be empty).
	Name string
	// Enabled gates whether the rule is compiled into the active Sieve
	// script. Disabled rules are preserved but not executed.
	Enabled bool
	// SortOrder controls the execution order: lower values run first.
	// Ties are broken by ID ascending.
	SortOrder int
	// Conditions is the parsed condition list. Stored as JSON in the DB.
	Conditions []RuleCondition
	// Actions is the parsed action list. Stored as JSON in the DB.
	Actions []RuleAction
	// CreatedAt is when the row was inserted.
	CreatedAt time.Time
	// UpdatedAt is when the row was last modified.
	UpdatedAt time.Time
}

// ManagedRuleFilter constrains ListManagedRules results.
type ManagedRuleFilter struct {
	// AfterID, when non-zero, returns only rows with id > AfterID
	// (ascending pagination).
	AfterID ManagedRuleID
	// Limit caps the page size. Zero means 256.
	Limit int
}
