//go:build darwin

package accessibility

import (
	"fmt"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/go-macos/objc"
)

// Generic element access: walk another application's AX tree, find a control,
// set its value, press it, and type into it with real key events. These are the
// pieces a GUI test driver needs; none of them is specific to any application.

var (
	elemOnce sync.Once
	elemErr  error

	strAXChildren, strAXValue, strAXDescription, strAXIdentifier, strAXPress, strAXFocused uintptr

	cfStringGetTypeID           func() uint64
	cgEventCreateKeyboardEvent  func(source uintptr, key uint16, down bool) uintptr
	cgEventKeyboardSetUnicode   func(ev uintptr, n uint64, chars *uint16)
	cgEventSetFlags             func(ev uintptr, flags uint64)
	cgEventPostToPid            func(pid int32, ev uintptr)
	cgEventPost                 func(tap uint32, ev uintptr)
	cgEventCreateMouseEvent     func(source uintptr, typ uint32, pos cgPoint, button uint32) uintptr
	cgEventSetIntegerValueField func(ev uintptr, field uint32, v int64)
)

func loadElement() error {
	if err := load(); err != nil {
		return err
	}
	elemOnce.Do(func() {
		as, err := purego.Dlopen(frameworkApplicationServices, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			elemErr = fmt.Errorf("accessibility: loading ApplicationServices: %w", err)
			return
		}
		purego.RegisterLibFunc(&cfStringGetTypeID, as, "CFStringGetTypeID")
		purego.RegisterLibFunc(&cgEventCreateKeyboardEvent, as, "CGEventCreateKeyboardEvent")
		purego.RegisterLibFunc(&cgEventKeyboardSetUnicode, as, "CGEventKeyboardSetUnicodeString")
		purego.RegisterLibFunc(&cgEventSetFlags, as, "CGEventSetFlags")
		purego.RegisterLibFunc(&cgEventPostToPid, as, "CGEventPostToPid")
		purego.RegisterLibFunc(&cgEventPost, as, "CGEventPost")
		purego.RegisterLibFunc(&cgEventCreateMouseEvent, as, "CGEventCreateMouseEvent")
		purego.RegisterLibFunc(&cgEventSetIntegerValueField, as, "CGEventSetIntegerValueField")
		pool(func() {
			keep := func(s string) uintptr { return cfRetain(uintptr(objc.NSString(s))) }
			strAXChildren = keep("AXChildren")
			strAXValue = keep("AXValue")
			strAXDescription = keep("AXDescription")
			strAXIdentifier = keep("AXIdentifier")
			strAXPress = keep("AXPress")
			strAXFocused = keep("AXFocused")
		})
	})
	return elemErr
}

// Element is a live handle on one accessibility element of another process.
// Close it when done; the reference is not garbage collected.
type Element struct {
	mu sync.Mutex
	el uintptr
}

// ApplicationElement returns the root element of the process pid.
func ApplicationElement(pid int) (*Element, error) {
	if err := loadElement(); err != nil {
		return nil, err
	}
	el := axUIElementCreateApplication(int32(pid))
	if el == 0 {
		return nil, fmt.Errorf("accessibility: AXUIElementCreateApplication(%d) returned nothing", pid)
	}
	return &Element{el: el}, nil
}

// Close releases the reference. It is safe to call more than once.
func (e *Element) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.el == 0 {
		return ErrClosed
	}
	cfRelease(e.el)
	e.el = 0
	return nil
}

// stringAttr reads an attribute as text: a CFString verbatim, anything else
// (number, boolean, AXValue) by its description. A missing attribute is "".
func stringAttr(el, attr uintptr) string {
	s, _ := attrString(el, attr)
	return s
}

// attrString is stringAttr with the AX status, so a caller deciding whether a
// field is secret can tell "has no such attribute" from "could not be read".
func attrString(el, attr uintptr) (string, AXError) {
	var ref uintptr
	st := AXError(axUIElementCopyAttributeValue(el, attr, &ref))
	if st != AXSuccess || ref == 0 {
		return "", st
	}
	defer cfRelease(ref)
	var s string
	pool(func() {
		if cfGetTypeID(ref) == cfStringGetTypeID() {
			s = objc.GoString(objc.ID(ref))
		} else {
			s = objc.GoString(objc.ID(objc.ID(ref).Send(objc.Sel("description"))))
		}
	})
	return s, AXSuccess
}

// secureLocked reports whether the element is, or may be, a password field.
// FAIL CLOSED: if its role or subrole cannot be read (a busy application
// answers kAXErrorCannotComplete), it counts as secure; only "no such
// attribute" and "no value" count as a plain answer.
func secureLocked(el uintptr) bool {
	for _, a := range []uintptr{strAXRole, strAXSubrole} {
		s, st := attrString(el, a)
		if st != AXSuccess && st != AXNoValue && st != AXAttributeUnsupported {
			return true
		}
		if s == SecureSubrole {
			return true
		}
	}
	return false
}

// Role, Subrole, Title, Description and Identifier read one attribute; an
// element that has no such attribute answers "".
func (e *Element) Role() string        { return e.str(strAXRole) }
func (e *Element) Subrole() string     { return e.str(strAXSubrole) }
func (e *Element) Title() string       { return e.str(strAXTitle) }
func (e *Element) Description() string { return e.str(strAXDescription) }
func (e *Element) Identifier() string  { return e.str(strAXIdentifier) }

// Value reads kAXValue, or "" for an element that is, or may be, a secure text
// field (see [Element.Secure]). It does NOT know about toolkits that give a
// password field no secure subrole (Qt's): for those, never print Value; use
// [Element.Snapshot] with RedactEditable for anything that is shown or logged.
func (e *Element) Value() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.el == 0 || secureLocked(e.el) {
		return ""
	}
	return stringAttr(e.el, strAXValue)
}

// Secure reports whether the element is a password field, or could not be
// read well enough to say it is not.
func (e *Element) Secure() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.el != 0 && secureLocked(e.el)
}

func (e *Element) str(attr uintptr) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.el == 0 {
		return ""
	}
	return stringAttr(e.el, attr)
}

// Children returns handles on kAXChildren. The caller must Close each.
func (e *Element) Children() ([]*Element, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.el == 0 {
		return nil, ErrClosed
	}
	var arr uintptr
	if err := AXError(axUIElementCopyAttributeValue(e.el, strAXChildren, &arr)); err != AXSuccess {
		if err == AXNoValue || err == AXAttributeUnsupported {
			return nil, nil
		}
		return nil, err.Err("reading kAXChildrenAttribute")
	}
	if arr == 0 {
		return nil, nil
	}
	defer cfRelease(arr)
	if cfGetTypeID(arr) != cfArrayGetTypeID() {
		return nil, nil
	}
	n := cfArrayGetCount(arr)
	out := make([]*Element, 0, n)
	for i := int64(0); i < n; i++ {
		if c := cfArrayGetValueAtIndex(arr, i); c != 0 {
			out = append(out, &Element{el: cfRetain(c)})
		}
	}
	return out, nil
}

// SnapshotLimits bounds a [Snapshot]; a tree is attacker-shaped input as far as
// the walker is concerned (an application may expose a cycle or a huge model).
type SnapshotLimits struct {
	// MaxDepth is the deepest level copied (the root is 0). Zero means 12.
	MaxDepth int
	// MaxNodes is the most elements copied. Zero means 5000.
	MaxNodes int
	// RedactEditable withholds the value of every editable control
	// (AXTextField, AXTextArea, AXComboBox), not only of the ones that say
	// they are secure. Toolkits do not always set the secure subrole on a
	// password field (Qt's does not here), so a dump taken while a secret is
	// typed must set this.
	RedactEditable bool
}

func editable(role string) bool {
	return role == "AXTextField" || role == "AXTextArea" || role == "AXComboBox" || role == "AXSecureTextField"
}

// Snapshot copies the element and its descendants into a [Node] tree. Secure
// fields are marked and their value is never read.
func (e *Element) Snapshot(lim SnapshotLimits) (*Node, error) {
	if lim.MaxDepth <= 0 {
		lim.MaxDepth = 12
	}
	if lim.MaxNodes <= 0 {
		lim.MaxNodes = 5000
	}
	budget := lim.MaxNodes
	e.mu.Lock()
	closed := e.el == 0
	e.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	return e.snapshot(0, lim, &budget)
}

func (e *Element) snapshot(depth int, lim SnapshotLimits, budget *int) (*Node, error) {
	maxDepth := lim.MaxDepth
	*budget--
	n := &Node{
		Role: e.Role(), Subrole: e.Subrole(), Title: e.Title(),
		Description: e.Description(), Identifier: e.Identifier(),
	}
	n.Secure = e.Secure() || n.Subrole == SecureSubrole || (lim.RedactEditable && editable(n.Role))
	if !n.Secure {
		n.Value = e.str(strAXValue)
	}
	if depth >= maxDepth || *budget <= 0 {
		return n, nil
	}
	kids, err := e.Children()
	if err != nil {
		return n, nil // an element that will not list children is a leaf
	}
	for _, k := range kids {
		if *budget > 0 {
			c, _ := k.snapshot(depth+1, lim, budget)
			n.Children = append(n.Children, *c)
		}
		_ = k.Close()
	}
	return n, nil
}

// Path is a way to reach an element again: the child indexes from the root.
type Path []int

// Locate returns every element under e (inclusive) for which match is true,
// with the path to each. The caller must Close each returned element.
func (e *Element) Locate(match func(*Node) bool, lim SnapshotLimits) ([]*Element, error) {
	tree, err := e.Snapshot(lim)
	if err != nil {
		return nil, err
	}
	var paths []Path
	var rec func(n *Node, p Path)
	rec = func(n *Node, p Path) {
		if match(n) {
			paths = append(paths, append(Path(nil), p...))
		}
		for i := range n.Children {
			rec(&n.Children[i], append(p, i))
		}
	}
	rec(tree, nil)
	var out []*Element
	for _, p := range paths {
		el, err := e.At(p)
		if err != nil {
			continue
		}
		out = append(out, el)
	}
	return out, nil
}

// At follows a path of child indexes from e. The caller must Close the result.
func (e *Element) At(p Path) (*Element, error) {
	cur := &Element{}
	e.mu.Lock()
	if e.el == 0 {
		e.mu.Unlock()
		return nil, ErrClosed
	}
	cur.el = cfRetain(e.el)
	e.mu.Unlock()
	for _, i := range p {
		kids, err := cur.Children()
		_ = cur.Close()
		if err != nil {
			return nil, err
		}
		if i < 0 || i >= len(kids) {
			CloseElements(kids)
			return nil, fmt.Errorf("accessibility: child %d out of range (%d children)", i, len(kids))
		}
		cur = kids[i]
		for j, k := range kids {
			if j != i {
				_ = k.Close()
			}
		}
	}
	return cur, nil
}

// CloseElements releases a list.
func CloseElements(es []*Element) {
	for _, e := range es {
		_ = e.Close()
	}
}

// SetValue writes kAXValue as text. Some secure fields refuse this (the error
// says so); [TypeText] is the fallback that goes through the keyboard.
func (e *Element) SetValue(s string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.el == 0 {
		return ErrClosed
	}
	var err error
	pool(func() {
		err = AXError(axUIElementSetAttributeValue(e.el, strAXValue, uintptr(objc.NSString(s)))).
			Err("setting kAXValueAttribute")
	})
	return err
}

// Press performs kAXPressAction.
func (e *Element) Press() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.el == 0 {
		return ErrClosed
	}
	return AXError(axUIElementPerformAction(e.el, strAXPress)).Err("performing kAXPressAction")
}

// Focus sets kAXFocused on the element.
func (e *Element) Focus() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.el == 0 {
		return ErrClosed
	}
	return AXError(axUIElementSetAttributeValue(e.el, strAXFocused, numberYes)).Err("setting kAXFocusedAttribute")
}

// Key codes for [PressKey] (ANSI layout virtual key codes).
const (
	KeyReturn = 36
	KeyTab    = 48
	KeySpace  = 49
	KeyEscape = 53
)

// TypeText posts text as keyboard events to the process pid, one Unicode unit
// at a time, with a pause between keys. The password never appears anywhere but
// in the event.
func TypeText(pid int, text string, pause time.Duration) error {
	if err := loadElement(); err != nil {
		return err
	}
	for _, r := range text {
		// One event per character: a character outside the BMP is two UTF-16
		// units that must travel together.
		units := utf16.Encode([]rune{r})
		for _, down := range []bool{true, false} {
			ev := cgEventCreateKeyboardEvent(0, 0, down)
			if ev == 0 {
				return fmt.Errorf("accessibility: CGEventCreateKeyboardEvent returned nothing")
			}
			cgEventSetFlags(ev, 0)
			cgEventKeyboardSetUnicode(ev, uint64(len(units)), &units[0])
			cgEventPostToPid(int32(pid), ev)
			cfRelease(ev)
		}
		time.Sleep(pause)
	}
	return nil
}

// PressKey posts one key press (down then up) with the key code to pid.
func PressKey(pid int, code uint16) error {
	if err := loadElement(); err != nil {
		return err
	}
	for _, down := range []bool{true, false} {
		ev := cgEventCreateKeyboardEvent(0, code, down)
		if ev == 0 {
			return fmt.Errorf("accessibility: CGEventCreateKeyboardEvent returned nothing")
		}
		cgEventSetFlags(ev, 0)
		cgEventPostToPid(int32(pid), ev)
		cfRelease(ev)
	}
	return nil
}

// Frame reads the element's position and size in global display coordinates.
func (e *Element) Frame() (Rect, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.el == 0 {
		return Rect{}, ErrClosed
	}
	var p cgPoint
	var s cgSize
	if err := readAXValue(e.el, strAXPosition, axValueTypeCGPoint, unsafe.Pointer(&p), "reading kAXPositionAttribute"); err != nil {
		return Rect{}, err
	}
	if err := readAXValue(e.el, strAXSize, axValueTypeCGSize, unsafe.Pointer(&s), "reading kAXSizeAttribute"); err != nil {
		return Rect{}, err
	}
	return Rect{p.X, p.Y, s.W, s.H}, nil
}

// Mouse event constants (CGEventType, CGMouseButton, CGEventField).
const (
	cgEventLeftMouseDown uint32 = 1
	cgEventLeftMouseUp   uint32 = 2
	cgMouseButtonLeft    uint32 = 0
	cgMouseEventClickSt  uint32 = 1
)

// ClickAt posts a left click of count clicks (2 for a double click) at the
// global point p to the process pid. The point must be over a visible part of
// the target's window; the click is delivered to pid only, but the pointer is
// not moved on screen.
func ClickAt(pid int, p Point, count int) error {
	if err := loadElement(); err != nil {
		return err
	}
	if count < 1 {
		count = 1
	}
	pos := cgPoint{p.X, p.Y}
	for i := 1; i <= count; i++ {
		for _, typ := range []uint32{cgEventLeftMouseDown, cgEventLeftMouseUp} {
			ev := cgEventCreateMouseEvent(0, typ, pos, cgMouseButtonLeft)
			if ev == 0 {
				return fmt.Errorf("accessibility: CGEventCreateMouseEvent returned nothing")
			}
			cgEventSetIntegerValueField(ev, cgMouseEventClickSt, int64(i))
			if pid <= 0 {
				cgEventPost(0, ev) // kCGHIDEventTap: whatever is under the point
			} else {
				cgEventPostToPid(int32(pid), ev)
			}
			cfRelease(ev)
		}
		time.Sleep(40 * time.Millisecond)
	}
	return nil
}

// Click clicks the centre of the element (count 2 = double click) in process pid.
func (e *Element) Click(pid, count int) error {
	f, err := e.Frame()
	if err != nil {
		return err
	}
	return ClickAt(pid, f.Center(), count)
}
