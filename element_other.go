//go:build !darwin

package accessibility

import "time"

// Element is a live handle on one accessibility element. There is none on this
// platform; every constructor reports [ErrUnsupported].
type Element struct{}

// Path is a way to reach an element again: the child indexes from the root.
type Path []int

// SnapshotLimits bounds a [Snapshot].
type SnapshotLimits struct {
	MaxDepth int
	MaxNodes int
}

// Key codes for [PressKey].
const (
	KeyReturn = 36
	KeyTab    = 48
	KeySpace  = 49
	KeyEscape = 53
)

// ApplicationElement reports [ErrUnsupported].
func ApplicationElement(pid int) (*Element, error) { return nil, ErrUnsupported }

// Close does nothing.
func (e *Element) Close() error { return ErrClosed }

// Role, Subrole, Title, Description, Identifier and Value answer "".
func (e *Element) Role() string        { return "" }
func (e *Element) Subrole() string     { return "" }
func (e *Element) Title() string       { return "" }
func (e *Element) Description() string { return "" }
func (e *Element) Identifier() string  { return "" }
func (e *Element) Value() string       { return "" }

// Secure is always false.
func (e *Element) Secure() bool { return false }

// Children reports [ErrUnsupported].
func (e *Element) Children() ([]*Element, error) { return nil, ErrUnsupported }

// Snapshot reports [ErrUnsupported].
func (e *Element) Snapshot(SnapshotLimits) (*Node, error) { return nil, ErrUnsupported }

// Locate reports [ErrUnsupported].
func (e *Element) Locate(func(*Node) bool, SnapshotLimits) ([]*Element, error) {
	return nil, ErrUnsupported
}

// At reports [ErrUnsupported].
func (e *Element) At(Path) (*Element, error) { return nil, ErrUnsupported }

// CloseElements does nothing.
func CloseElements([]*Element) {}

// SetValue reports [ErrUnsupported].
func (e *Element) SetValue(string) error { return ErrUnsupported }

// Press reports [ErrUnsupported].
func (e *Element) Press() error { return ErrUnsupported }

// Focus reports [ErrUnsupported].
func (e *Element) Focus() error { return ErrUnsupported }

// TypeText reports [ErrUnsupported].
func TypeText(int, string, time.Duration) error { return ErrUnsupported }

// PressKey reports [ErrUnsupported].
func PressKey(int, uint16) error { return ErrUnsupported }
