package spam

// AuthStatus mirrors internal/sieve.AuthStatus. We intentionally
// redeclare it at the consumer rather than importing internal/sieve,
// because the spam package is upstream of delivery routing and must not
// pull the Sieve interpreter into its compile-time surface.
//
// The authoritative type is mailauth.AuthStatus (parallel wave). Its
// String method and enum values align with ours by shape, which lets
// the concrete mailauth.AuthResults satisfy AuthResultsReader directly.
type AuthStatus int

// Authentication outcome values. "Pass" is the only one that shortens
// the classifier prompt; every other value is distilled to a boolean
// "did not pass" and paired with the method name.
const (
	AuthNone AuthStatus = iota
	AuthPass
	AuthFail
	AuthSoftFail
	AuthNeutral
	AuthTempError
	AuthPermError
	AuthPolicy
)

// AuthResultsReader is the minimum authentication-result surface the
// prompt builder needs. The concrete mailauth.AuthResults satisfies it
// naturally.
type AuthResultsReader interface {
	SPF() AuthStatus
	DKIM() AuthStatus
	DMARC() AuthStatus
	ARC() AuthStatus
	FromDomain() string
}

// nilAuthResults is the fallback used when Classify receives a nil
// AuthResultsReader so we never dereference nil.
type nilAuthResults struct{}

func (nilAuthResults) SPF() AuthStatus    { return AuthNone }
func (nilAuthResults) DKIM() AuthStatus   { return AuthNone }
func (nilAuthResults) DMARC() AuthStatus  { return AuthNone }
func (nilAuthResults) ARC() AuthStatus    { return AuthNone }
func (nilAuthResults) FromDomain() string { return "" }
