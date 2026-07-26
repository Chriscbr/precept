package fixture

// INVARIANT: a free-standing claim is associated with the package

var Separator int

// PRECONDITION: variables can be direct subjects
var Variable int

const (
	// INVARIANT: constants can be direct subjects
	Limit = 10
)

// POSTCONDITION: a grouped declaration comment falls back to the package
type (
	Plain int
)

var Trailing int // INVARIANT: trailing value comments use the value as their subject

// invariant: lowercase INVARIANT is not accepted
var LowercaseInvariant int

/* PRECONDITION: block comments are ignored */
var BlockComment int

func InvalidConditionBoundary() {
	// PRECONDITION: valid claim before an invalid condition
	//POSTCONDITION: this invalid spelling is ignored, not appended
	//  POSTCONDITION: two separator spaces are ignored too
	// this line is ignored too
}
