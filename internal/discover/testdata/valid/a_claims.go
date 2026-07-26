// INVARIANT: package documentation is associated with the package
package fixture

// PRECONDITION: function documentation is associated with the function
func Documented() {}

func InBody(value int) int {
	// INVARIANT: claims in a function body are associated with the function
	return value
}

type Cache struct{}

func (*Cache) Get(value int) int {
	// POSTCONDITION: claims in a method body are associated with the method
	return value
}

// INVARIANT: named types remain supported
type Named struct {
	// INVARIANT: field documentation is associated with the field
	Value int

	Count int // INVARIANT: trailing field comments are associated with the field
}

type (
	// PRECONDITION: grouped type specifications are associated with the named type
	Grouped struct{}
)

func Multiple() {
	// PRECONDITION: first claim
	// POSTCONDITION: second claim
	// with a continuation
}

// INVARIANT:
// an empty marker can use a continuation
func ContinuationOnly() {}

// PRECONDITION:
func EmptyPrecondition() {}

func EmptyInvariant() {
	// INVARIANT:
}

// POSTCONDITION: locations use physical source lines
//
//line virtual_source.go:100
func PhysicalLines() {}
