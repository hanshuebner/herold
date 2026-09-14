package sieve

// rulematch.go evaluates a ManagedRule's conditions directly against a
// parsed message, without compiling or running Sieve. It exists for two
// call sites that must decide whether a rule matches without the compiled-
// and-executed script (internal/sieve.Parse/Validate/Execute) being
// available:
//
//   - the IMAP-import path, which never runs Sieve at all
//     (REQ-IMAP-IMP-31) but still needs to honour a "never-spam" managed
//     rule (REQ-FILT-02a / REQ-FLT-16, issue #382) when it applies the
//     classifier's verdict-to-mailbox mapping directly in Go
//     (internal/imapimport/spam.go resolveImportSpamTarget);
//   - the SMTP delivery path's transparency record, which needs to name
//     the rule responsible for a delivered-not-junked spam/suspect
//     verdict (REQ-FILT-66) without re-deriving that from the already-
//     executed sieve.Outcome, which carries no per-rule provenance.
//
// matchSingleCondition mirrors compileSingleCondition field-for-field: a
// rule that would compile to a matching Sieve test also matches here, and
// the two are exercised against the same cases in compile_managed_test.go
// / rulematch_test.go to keep them from drifting apart.
import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// MatchRuleConditions reports whether msg satisfies every condition in
// conds (AND-combined, mirroring compileConditions). An empty condition
// list matches unconditionally, mirroring compileConditions' "true".
func MatchRuleConditions(conds []store.RuleCondition, msg mailparse.Message) (bool, error) {
	for _, c := range conds {
		ok, err := matchSingleCondition(c, msg)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// matchSingleCondition evaluates one RuleCondition against msg using the
// same field/op semantics compileSingleCondition compiles into Sieve.
func matchSingleCondition(c store.RuleCondition, msg mailparse.Message) (bool, error) {
	if err := validateConditionField(c.Field); err != nil {
		return false, err
	}
	if err := validateConditionOp(c.Op); err != nil {
		return false, err
	}

	m := matcher{comparator: "i;ascii-casemap", addressPart: ":all", matchType: compileMatchOp(c.Op)}

	switch c.Field {
	case "from":
		return matchAddressHeader(msg, "From", m, c.Value)
	case "to":
		return matchAddressHeader(msg, "To", m, c.Value)
	case "subject":
		return matchHeader(msg, "Subject", m, c.Value)
	case "has-attachment":
		// Mirrors compileSingleCondition: op/value are ignored.
		hm := matcher{comparator: "i;ascii-casemap", matchType: ":contains"}
		return matchHeader(msg, "Content-Type", hm, "multipart")
	case "thread-id":
		return matchHeader(msg, XHeroldThreadIDHeader, m, c.Value)
	case "from-domain":
		// Mirrors compileSingleCondition's `address :matches :domain
		// "From" "*@<value>"` exactly, op included: from-domain always
		// wildcard-matches regardless of the condition's own Op.
		dm := matcher{comparator: "i;ascii-casemap", matchType: ":matches", addressPart: ":domain"}
		return matchAddressHeader(msg, "From", dm, "*@"+c.Value)
	default:
		return false, fmt.Errorf("unknown condition field %q", c.Field)
	}
}

// matchHeader reports whether any value of the named header compares true
// against key under m.
func matchHeader(msg mailparse.Message, name string, m matcher, key string) (bool, error) {
	for _, v := range msg.Headers.GetAll(name) {
		ok, err := compare(m, strings.TrimSpace(v), key)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// matchAddressHeader reports whether any address extracted (per
// m.addressPart) from the named header compares true against key under m.
func matchAddressHeader(msg mailparse.Message, name string, m matcher, key string) (bool, error) {
	for _, v := range msg.Headers.GetAll(name) {
		for _, addr := range extractAddressParts(v, m.addressPart) {
			ok, err := compare(m, addr, key)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
	}
	return false, nil
}

// NeverSpamOverrideLabel scans rules (in order) for the first enabled rule
// carrying a NeverSpamActionKind action whose conditions match msg, and
// returns the transparency-record label for it: "filter:<name>" when the
// rule is named, "filter:<id>" otherwise. Returns "" when no such rule
// matches. rules need not be pre-filtered -- disabled rules and rules
// without a never-spam action are skipped.
func NeverSpamOverrideLabel(rules []store.ManagedRule, msg mailparse.Message) (string, error) {
	for _, r := range rules {
		if !r.Enabled || !hasNeverSpamAction(r.Actions) {
			continue
		}
		ok, err := MatchRuleConditions(r.Conditions, msg)
		if err != nil {
			return "", err
		}
		if !ok {
			continue
		}
		if r.Name != "" {
			return "filter:" + r.Name, nil
		}
		return "filter:" + strconv.FormatInt(int64(r.ID), 10), nil
	}
	return "", nil
}
