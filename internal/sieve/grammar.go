package sieve

// The AST types and parser live in one package so that the parse-state
// machine (tokens → AST) can access unexported helpers directly. Callers
// never construct these types by hand; use Parse.

// Script is a parsed Sieve program: an optional run of require declarations
// followed by zero or more commands at the top level.
type Script struct {
	Requires []string
	Commands []Command
}

// Command is one Sieve command: its identifier (e.g. "if", "fileinto"),
// positional arguments (tags + string-lists + numbers), an optional test
// (for control-flow commands), and an optional block of nested commands.
type Command struct {
	// Name is the command identifier lower-cased for case-insensitive
	// matching per RFC 5228 §2.10.
	Name string
	// Args are the command arguments in source order, arity validated
	// per-command during semantic validation.
	Args []Argument
	// Test is set for control-flow commands (if / elsif / while variants
	// don't exist in Sieve base; only if and elsif / else take a test —
	// else has no test). Nil otherwise.
	Test *Test
	// Block is the nested command block in braces. Nil for leaf commands.
	Block []Command
	// Line is the 1-based source line of the command name for diagnostics.
	Line int
	// Column is the 1-based source column of the command name.
	Column int
}

// Test is an element of the test tree: a named test with arguments, or one
// of the boolean combinators allof / anyof / not / true / false.
type Test struct {
	// Name is the test identifier lower-cased.
	Name string
	// Args are the positional arguments to the test.
	Args []Argument
	// Children are the sub-tests for allof / anyof / not.
	Children []Test
	// Line is the 1-based source line of the test name.
	Line int
	// Column is the 1-based source column of the test name.
	Column int
}

// ArgKind enumerates the shapes an Argument may carry.
type ArgKind int

// Argument shapes. Sieve has a narrow set: string literals, numbers
// (optionally quantity-suffixed), tagged identifiers like ":is", and
// string-lists built with square brackets.
const (
	ArgInvalid ArgKind = iota
	ArgTag
	ArgString
	ArgNumber
	ArgStringList
)

// Argument is a single argument to a command or test. The Kind field
// discriminates which of the value slots is populated.
type Argument struct {
	Kind    ArgKind
	Tag     string   // ArgTag, includes the leading ':'
	Str     string   // ArgString, already de-quoted and encoded-char expanded
	Num     int64    // ArgNumber, normalised to bytes when a quantity suffix was present
	StrList []string // ArgStringList, each entry de-quoted
	// Line/Column point at the argument's first token.
	Line   int
	Column int
}
