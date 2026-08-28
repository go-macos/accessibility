// Package accessibility moves another application's window to a chosen place
// on macOS, from pure Go with CGO_ENABLED=0.
//
// The macOS Accessibility (AX) API is the only supported way for one process to
// move another process's window, and it is what this package binds — through
// purego, so nothing here needs cgo.
//
// # What it is for
//
// The consumer is an XR virtual-desktop app that puts several displays on a
// 360° ribbon around the wearer. "Put this application on ribbon position 3"
// has to become "move that window onto that display", without the wearer
// dragging anything across a display boundary by hand.
//
// # The coordinate space
//
// Every rectangle in this package is in GLOBAL DISPLAY COORDINATES: the origin
// is the top-left corner of the main display and y increases DOWNWARDS. That is
// the space of kAXPositionAttribute, of CGDisplayBounds and of
// CGWindowListCopyWindowInfo — three independent instruments that agree. It is
// NOT NSScreen's space, whose origin is at the bottom-left. Mixing the two is
// the classic way to send a window to a plausible-looking wrong place, so
// NSScreen is not consulted anywhere in this package. (It is also a cache; see
// go-macos/virtualdisplay.)
//
// # Permission
//
// Unlike go-macos/hotkey, this package DOES need the Accessibility (TCC) grant.
// There is no equivalent of Carbon's permission-free route: AX is the API, and
// AX is gated. [Trusted] reports the state without side effects; [RequestTrust]
// is the only call that shows the system dialog, and a caller has to ask for it
// on purpose. See [Trust] for what a refusal usually means, which on macOS is
// rarely what it first looks like.
//
// # Portability
//
// Every exported symbol exists on every platform, so a consumer cross-compiles
// without build tags of its own; off darwin the operating-system calls report
// [ErrUnsupported]. The whole placement policy — the rectangle arithmetic, the
// choice of display, the clamping, and the read-back check that decides whether
// a move actually happened — is OS-independent and is exercised in full on
// Linux, with no AX anywhere in sight.
package accessibility

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Errors reported by this package. They are stable and may be tested with
// errors.Is.
var (
	// ErrUnsupported is returned by every operating-system call on
	// non-darwin platforms.
	ErrUnsupported = errors.New("accessibility: unsupported on this platform (darwin only)")

	// ErrNotTrusted reports that this process does not hold the
	// Accessibility (TCC) grant, so AX will not answer for other
	// applications. Call [Status] and show [Trust.Advice] to the user: on
	// macOS the grant is usually held by a parent application rather than
	// by the binary that is running.
	ErrNotTrusted = errors.New("accessibility: this process is not trusted for Accessibility")

	// ErrNoDisplays reports that no display was found to place a window on.
	ErrNoDisplays = errors.New("accessibility: no displays")

	// ErrNoWindow reports that the application has no window that can be
	// moved — it may have none, or only windows AX declines to describe.
	ErrNoWindow = errors.New("accessibility: no movable window")

	// ErrRefused reports that the write was accepted — AXError 0, no
	// complaint from anyone — and the window nevertheless did not go where
	// it was told. This is the failure that matters: a status check alone
	// cannot see it, which is why [Move] reads the window back and returns
	// this instead of pretending.
	ErrRefused = errors.New("accessibility: the window did not move where it was told")

	// ErrClosed reports use of a window handle that has already been
	// released.
	ErrClosed = errors.New("accessibility: window handle already released")
)

// ---------------------------------------------------------------------------
// Geometry.
// ---------------------------------------------------------------------------

// Point is a position in global display coordinates.
type Point struct{ X, Y float64 }

// String renders the point for a log line.
func (p Point) String() string { return fmt.Sprintf("(%g,%g)", p.X, p.Y) }

// Size is a width and a height in points.
type Size struct{ W, H float64 }

// String renders the size for a log line.
func (s Size) String() string { return fmt.Sprintf("%gx%g", s.W, s.H) }

// Rect is a rectangle in global display coordinates: origin top-left, y
// increasing downwards. See the package comment on the coordinate space.
type Rect struct{ X, Y, W, H float64 }

// Origin returns the top-left corner.
func (r Rect) Origin() Point { return Point{r.X, r.Y} }

// Size returns the width and height.
func (r Rect) Size() Size { return Size{r.W, r.H} }

// Right returns the x coordinate just past the right-hand edge.
func (r Rect) Right() float64 { return r.X + r.W }

// Bottom returns the y coordinate just past the bottom edge.
func (r Rect) Bottom() float64 { return r.Y + r.H }

// Center returns the middle of the rectangle.
func (r Rect) Center() Point { return Point{r.X + r.W/2, r.Y + r.H/2} }

// Empty reports whether the rectangle encloses nothing.
func (r Rect) Empty() bool { return r.W <= 0 || r.H <= 0 }

// Area returns the enclosed area, or zero for an empty rectangle.
func (r Rect) Area() float64 {
	if r.Empty() {
		return 0
	}
	return r.W * r.H
}

// Contains reports whether p lies inside the rectangle. The top and left edges
// are inside, the bottom and right edges are not, so adjacent displays never
// both claim the same point.
func (r Rect) Contains(p Point) bool {
	return p.X >= r.X && p.X < r.Right() && p.Y >= r.Y && p.Y < r.Bottom()
}

// Intersect returns the overlap of two rectangles, or the zero Rect when they
// do not overlap.
func (r Rect) Intersect(o Rect) Rect {
	x := math.Max(r.X, o.X)
	y := math.Max(r.Y, o.Y)
	w := math.Min(r.Right(), o.Right()) - x
	h := math.Min(r.Bottom(), o.Bottom()) - y
	if w <= 0 || h <= 0 {
		return Rect{}
	}
	return Rect{x, y, w, h}
}

// Inset returns the rectangle shrunk by d on every side. A d that would leave
// nothing is refused: the rectangle comes back unchanged rather than empty,
// because an empty target display is never what a caller means.
func (r Rect) Inset(d float64) Rect {
	if d == 0 {
		return r
	}
	out := Rect{r.X + d, r.Y + d, r.W - 2*d, r.H - 2*d}
	if out.Empty() {
		return r
	}
	return out
}

// Offset returns the rectangle moved by (dx, dy).
func (r Rect) Offset(dx, dy float64) Rect { return Rect{r.X + dx, r.Y + dy, r.W, r.H} }

// NearlyEqual reports whether every edge of the two rectangles is within tol.
// Window managers round, snap and clamp, so exact equality is the wrong test.
func (r Rect) NearlyEqual(o Rect, tol float64) bool {
	return near(r.X, o.X, tol) && near(r.Y, o.Y, tol) &&
		near(r.W, o.W, tol) && near(r.H, o.H, tol)
}

// String renders the rectangle for a log line: "300,200 640x480".
func (r Rect) String() string { return fmt.Sprintf("%g,%g %gx%g", r.X, r.Y, r.W, r.H) }

// near reports whether a and b differ by no more than tol.
func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// ---------------------------------------------------------------------------
// Displays.
// ---------------------------------------------------------------------------

// Display is one active display, as CoreGraphics describes it.
type Display struct {
	// ID is the CGDirectDisplayID. It is stable while the display stays
	// attached and is what a caller should remember a ribbon position by.
	ID uint32
	// Bounds is the display's rectangle in global coordinates. The main
	// display's origin is (0,0) and every other display is placed relative
	// to it, so a display above or to the left of the main one has negative
	// coordinates.
	Bounds Rect
	// Main reports whether this is the main display — the one carrying the
	// menu bar, and the origin of the coordinate space.
	Main bool
}

// String renders the display for a log line.
func (d Display) String() string {
	if d.Main {
		return fmt.Sprintf("display %d [%s] (main)", d.ID, d.Bounds)
	}
	return fmt.Sprintf("display %d [%s]", d.ID, d.Bounds)
}

// DisplayByID finds a display by its CGDirectDisplayID.
func DisplayByID(displays []Display, id uint32) (Display, bool) {
	for _, d := range displays {
		if d.ID == id {
			return d, true
		}
	}
	return Display{}, false
}

// MainDisplay returns the main display.
func MainDisplay(displays []Display) (Display, bool) {
	for _, d := range displays {
		if d.Main {
			return d, true
		}
	}
	return Display{}, false
}

// DisplayFor reports which display a window is on: the one covering the most
// of it.
//
// Overlap area, not the window's origin, decides. A window straddling two
// displays has an origin on exactly one of them, and it is routinely the one
// showing the smaller sliver — a title bar dragged just past the seam. Answering
// with that display would make [MoveToDisplay] compute the wrong relative
// position and put the window somewhere the user did not ask for.
//
// A window that overlaps nothing at all — entirely off every display, which
// happens after a display is unplugged — is attributed to the display whose
// centre is nearest, so it can still be brought back. Ties go to the lowest ID
// so the answer is deterministic.
func DisplayFor(frame Rect, displays []Display) (Display, bool) {
	if len(displays) == 0 {
		return Display{}, false
	}
	best, bestArea := Display{}, -1.0
	for _, d := range displays {
		a := d.Bounds.Intersect(frame).Area()
		if a > bestArea || (a == bestArea && d.ID < best.ID) {
			best, bestArea = d, a
		}
	}
	if bestArea > 0 {
		return best, true
	}
	// Nothing overlaps: fall back to the nearest centre.
	best, bestDist := Display{}, math.Inf(1)
	c := frame.Center()
	for _, d := range displays {
		dc := d.Bounds.Center()
		dist := math.Hypot(dc.X-c.X, dc.Y-c.Y)
		if dist < bestDist || (dist == bestDist && d.ID < best.ID) {
			best, bestDist = d, dist
		}
	}
	return best, true
}

// ---------------------------------------------------------------------------
// Placement policy.
// ---------------------------------------------------------------------------

// Placement says where on the target display a window should land.
type Placement int

// The placements. The zero value, [Relative], is what a ribbon wants: the
// window keeps the position and proportion it had, so a window that filled the
// left half of one display fills the left half of the next.
const (
	// Relative keeps the window's position and size as a fraction of the
	// display it came from.
	Relative Placement = iota
	// Origin puts the window's top-left corner at the display's top-left
	// corner and leaves its size alone.
	Origin
	// Center centres the window on the display and leaves its size alone.
	Center
	// Fill makes the window cover the whole display.
	Fill
)

// String names the placement.
func (p Placement) String() string {
	switch p {
	case Relative:
		return "relative"
	case Origin:
		return "origin"
	case Center:
		return "center"
	case Fill:
		return "fill"
	}
	return fmt.Sprintf("placement(%d)", int(p))
}

// DefaultTolerance is how far, in points, a window may land from where it was
// told before [Move] calls that a refusal. A window manager rounds to whole
// points and some applications snap to a grid, so demanding an exact match
// would report a refusal for a move that plainly happened.
const DefaultTolerance = 2.0

// minAttempts is how many times [Move] writes the position before it starts
// demanding progress.
//
// Two is not padding. Setting the size can move the origin — a window whose new
// size no longer fits where it was put gets pushed back by the window server —
// so the position is written, then the size, then the position again if the
// read-back disagrees.
const minAttempts = 2

// maxAttempts is the ceiling on that loop.
//
// It used to be 2, with a comment saying a third attempt had never changed an
// outcome. That was WRONG, and measuring it was the whole point: a window too
// big for its destination is not refused by the window server, it is moved ONE
// DISPLAY CLOSER PER WRITE. Thunderbird, 1922 points wide, sent to the far end
// of a six-screen ribbon on macOS 26.6.2:
//
//	write 1  -> -1882   distance 9638
//	write 2  -> -3802   distance 7718
//	...
//	write 6  -> -11520  distance 0      ARRIVED
//
// So [Move] keeps writing while each write brings the window closer, and stops
// the moment one does not. The ceiling is a backstop against an application
// that oscillates, not a budget: an ordinary move arrives on the first write.
const maxAttempts = 16

// Options tunes [Move] and [MoveToDisplay]. The zero value is the sensible
// default: [Relative] placement, no inset, clamped to the target display,
// raised afterwards, [DefaultTolerance].
type Options struct {
	// Placement selects where on the target display the window lands.
	Placement Placement

	// Inset shrinks the target display's usable rectangle by this many
	// points on every side. Use it to keep clear of the menu bar or of a
	// ribbon's own furniture.
	Inset float64

	// NoClamp lets the window keep a size and position that hang off the
	// edge of the target display. By default a window is shrunk and nudged
	// until it fits, because a window half off a ribbon panel is not on that
	// ribbon panel.
	NoClamp bool

	// NoRaise leaves the window's stacking order alone. By default a move
	// also raises the window and makes its application frontmost, because
	// "send it there" nearly always means "and let me see it".
	NoRaise bool

	// Tolerance overrides [DefaultTolerance]: how far the window may land
	// from where it was told before the move counts as refused. A negative
	// value means the same as zero — exact.
	Tolerance float64
}

// placement returns the placement to use, tolerating a nil Options.
func (o *Options) placement() Placement {
	if o == nil {
		return Relative
	}
	return o.Placement
}

// inset returns the inset to use. A negative inset would grow the target beyond
// the display, so it is ignored.
func (o *Options) inset() float64 {
	if o == nil || o.Inset <= 0 {
		return 0
	}
	return o.Inset
}

// clamp reports whether the result must fit wholly on the target display.
func (o *Options) clamp() bool { return o == nil || !o.NoClamp }

// raise reports whether the move should also bring the window forward.
func (o *Options) raise() bool { return o == nil || !o.NoRaise }

// tolerance returns the read-back tolerance to use.
func (o *Options) tolerance() float64 {
	if o == nil || o.Tolerance == 0 {
		return DefaultTolerance
	}
	if o.Tolerance < 0 {
		return 0
	}
	return o.Tolerance
}

// Place computes where a window should go, and is the whole of this package's
// geometry policy: a pure function of two rectangles, with no operating system
// anywhere near it.
//
// from is the display the window is on now and to is the display it should end
// up on; they may be the same. Only [Relative] reads from at all, and a
// degenerate from — a display of zero width or height, which is what an
// unplugged display leaves behind — falls back to [Origin] rather than dividing
// by zero.
func Place(frame Rect, from, to Display, opts *Options) Rect {
	avail := to.Bounds.Inset(opts.inset())
	var out Rect
	switch p := opts.placement(); p {
	case Fill:
		out = avail
	case Center:
		out = Rect{avail.X + (avail.W-frame.W)/2, avail.Y + (avail.H-frame.H)/2, frame.W, frame.H}
	case Relative:
		if !from.Bounds.Empty() {
			fx := (frame.X - from.Bounds.X) / from.Bounds.W
			fy := (frame.Y - from.Bounds.Y) / from.Bounds.H
			fw := frame.W / from.Bounds.W
			fh := frame.H / from.Bounds.H
			out = Rect{avail.X + fx*avail.W, avail.Y + fy*avail.H, fw * avail.W, fh * avail.H}
			break
		}
		// A display with no extent: nothing relative can be computed from
		// it, so fall through to Origin rather than produce NaN.
		fallthrough
	default: // Origin, and any unknown placement
		out = Rect{avail.X, avail.Y, frame.W, frame.H}
	}
	if opts.clamp() {
		out = clampTo(out, avail)
	}
	return out
}

// clampTo shrinks and nudges r until it lies wholly inside avail. Size is
// reduced first — a window wider than the display cannot be nudged into it —
// and the origin is then pulled back from the far edge before being pushed off
// the near one, so a window that is exactly the size of the display lands on
// its origin rather than one point off it.
func clampTo(r, avail Rect) Rect {
	if avail.Empty() {
		return r
	}
	r.W = math.Min(r.W, avail.W)
	r.H = math.Min(r.H, avail.H)
	r.X = math.Min(r.X, avail.Right()-r.W)
	r.Y = math.Min(r.Y, avail.Bottom()-r.H)
	r.X = math.Max(r.X, avail.X)
	r.Y = math.Max(r.Y, avail.Y)
	return r
}

// ---------------------------------------------------------------------------
// The seam: a window that can be measured and moved.
// ---------------------------------------------------------------------------

// Window is the seam between the placement policy and the operating system.
// [Move] speaks only to this, so every branch of the policy — including a
// window that lies about where it went — is testable on any platform, with no
// AX at all.
//
// The darwin implementation is [*AXWindow]. Position and size are separate
// because AX has two attributes, kAXPositionAttribute and kAXSizeAttribute, and
// a caller that only wants to move a window should not be made to restate its
// size.
type Window interface {
	// Frame reads the window's current rectangle back from the system. It
	// is called both before and after a write, and it must really ask —
	// returning a remembered value would make the read-back check
	// worthless.
	Frame() (Rect, error)
	// SetPosition writes kAXPositionAttribute.
	SetPosition(Point) error
	// SetSize writes kAXSizeAttribute.
	SetSize(Size) error
	// Raise brings the window forward and makes its application frontmost.
	Raise() error
}

// Result is what a move actually achieved, measured rather than assumed.
type Result struct {
	// Before is where the window was, read before anything was written.
	Before Rect
	// Wanted is where [Place] decided it should go.
	Wanted Rect
	// Got is where it actually is, READ BACK after the write. This is the
	// only field that is evidence; the rest is intent.
	Got Rect
	// From and To are the displays the window came from and was sent to.
	// They are the zero Display when the caller used [Move] directly and
	// named no display.
	From, To Display
	// Attempts counts the position writes it took. See defaultAttempts for
	// why more than one is normal.
	Attempts int
	// Moved reports whether Got's origin is within tolerance of Wanted's.
	Moved bool
	// Resized reports whether Got's size is within tolerance of Wanted's. A
	// window with a minimum size refuses to shrink and this is false while
	// Moved is true, which is a success, not a failure.
	Resized bool
	// Raised reports whether the window was brought forward.
	Raised bool
}

// String renders the result the way a person needs to read it: what was asked
// for, what happened, and the difference between them.
func (r Result) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s → wanted %s, got %s", r.Before, r.Wanted, r.Got)
	if !r.Moved {
		fmt.Fprintf(&b, " (NOT MOVED: off by %g,%g)", r.Got.X-r.Wanted.X, r.Got.Y-r.Wanted.Y)
	} else if !r.Resized {
		fmt.Fprintf(&b, " (moved; size refused, %s not %s)", r.Got.Size(), r.Wanted.Size())
	}
	if r.Attempts > 1 {
		fmt.Fprintf(&b, " after %d attempts", r.Attempts)
	}
	return b.String()
}

// Move puts a window at want and then PROVES it, by reading the window back
// through [Window.Frame] and comparing.
//
// This is the point of the package. AX accepts a write to kAXPositionAttribute
// with AXError 0 whether or not the application honours it: a window pinned by
// its own controller, a full-screen window, a sheet, a window whose application
// is not answering — all of them return success and stay exactly where they
// were. A caller that checked the status would be told the move worked. So the
// status is not what is checked here; the window's position afterwards is.
//
// The size is written only when it differs from the window's current size, and
// the position is re-asserted when the first read-back disagrees — setting a
// size can push the origin back. If the window still is not where it was told,
// Move returns [ErrRefused] together with the Result, so a caller can both
// react to the failure and see exactly how far off it landed.
func Move(w Window, want Rect, opts *Options) (Result, error) {
	if w == nil {
		return Result{}, errors.New("accessibility: nil Window")
	}
	tol := opts.tolerance()
	before, err := w.Frame()
	if err != nil {
		return Result{}, fmt.Errorf("accessibility: reading the window before moving it: %w", err)
	}
	res := Result{Before: before, Wanted: want, Got: before}

	// closest is the smallest distance to the target seen so far. A write that
	// does not improve on it is a write that will never arrive.
	closest := offBy(res.Got, want)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		res.Attempts = attempt
		wrongSize := !near(res.Got.W, want.W, tol) || !near(res.Got.H, want.H, tol)
		// SHRINK BEFORE MOVING; GROW AFTER.
		//
		// The window server clamps a move that would put the window outside
		// the displays, and it judges by the size the window has AT THE MOMENT
		// OF THE WRITE. So a window wider than where it is going must be shrunk
		// first, or the move is silently corrected to something nobody asked
		// for. Measured on macOS 26.6.2, a 2056x1083 Thunderbird window sent to
		// fill a 1920x1080 screen at -9600,0:
		//
		//	position, then size -> 2040x1049 at -2016,40   (clamped, useless)
		//	size, then position -> 1920x1049 at -9600,31   (arrived)
		//
		// Growing is the other way round: a window told to grow where it
		// stands can be clamped there, so the position goes first and the
		// growth happens at the destination.
		//
		// The TOLERANCE HAS NO PART IN THIS. It says how close is close enough
		// to call a move done; it says nothing about whether the window FITS.
		// Measured: a window TWO points wider than its destination — inside the
		// default tolerance of 2 — was still clamped 11520 points away, because
		// the window server does not round in anyone's favour.
		tooBig := res.Got.W > want.W || res.Got.H > want.H
		if wrongSize && tooBig {
			if err := w.SetSize(want.Size()); err != nil {
				return res, fmt.Errorf("accessibility: setting the size: %w", err)
			}
		}
		if err := w.SetPosition(want.Origin()); err != nil {
			return res, fmt.Errorf("accessibility: setting the position: %w", err)
		}
		if wrongSize && !tooBig {
			if err := w.SetSize(want.Size()); err != nil {
				return res, fmt.Errorf("accessibility: setting the size: %w", err)
			}
		}
		got, err := w.Frame()
		if err != nil {
			return res, fmt.Errorf("accessibility: reading the window back after moving it: %w", err)
		}
		res.Got = got
		res.Moved = near(got.X, want.X, tol) && near(got.Y, want.Y, tol)
		res.Resized = near(got.W, want.W, tol) && near(got.H, want.H, tol)
		if res.Moved && res.Resized {
			break
		}
		// Stop as soon as a write stops helping. The first minAttempts are
		// spent regardless, because setting the size can push the origin back
		// and the second write is what puts it right.
		d := offBy(got, want)
		if attempt >= minAttempts && d >= closest-math.Max(tol, 1) {
			break
		}
		if d < closest {
			closest = d
		}
	}
	if res.Moved && opts.raise() {
		if err := w.Raise(); err != nil {
			return res, fmt.Errorf("accessibility: raising the window: %w", err)
		}
		res.Raised = true
	}
	if !res.Moved {
		return res, fmt.Errorf("%w: %s", ErrRefused, res)
	}
	return res, nil
}

// MoveToDisplay sends a window to a display: it works out which display the
// window is on now, asks [Place] where it should land on the target, and then
// [Move] proves it went there.
//
// The caller supplies the display list rather than this function fetching one,
// so the same list can be used for a whole batch of windows and so the policy
// stays a pure function. Pass [Displays] on darwin, or a list of your own —
// go-xrkit's ribbon positions are displays that this package never has to know
// the meaning of.
func MoveToDisplay(w Window, to Display, displays []Display, opts *Options) (Result, error) {
	if w == nil {
		return Result{}, errors.New("accessibility: nil Window")
	}
	if len(displays) == 0 {
		return Result{}, ErrNoDisplays
	}
	frame, err := w.Frame()
	if err != nil {
		return Result{}, fmt.Errorf("accessibility: reading the window before moving it: %w", err)
	}
	from, _ := DisplayFor(frame, displays) // len > 0, so this cannot fail
	res, err := Move(w, Place(frame, from, to, opts), opts)
	res.From, res.To = from, to
	return res, err
}

// ---------------------------------------------------------------------------
// What a listing looks like.
// ---------------------------------------------------------------------------

// WindowInfo is a snapshot of one window: enough to show a person a list and
// let them pick one.
type WindowInfo struct {
	// PID is the owning process.
	PID int
	// App is the application's localised name.
	App string
	// Title is the window's title, which is often empty — a document window
	// that has never been saved, or an application that does not set one.
	Title string
	// Frame is the window's rectangle in global coordinates.
	Frame Rect
	// Display is the CGDirectDisplayID of the display the window is mostly
	// on, or zero when it was not resolved. Fill it with [Annotate].
	Display uint32
	// Minimized reports whether the window is in the Dock. A minimized
	// window still has a position and can still be moved, and will appear
	// where it was put when it is restored.
	Minimized bool
}

// String renders the window for a listing.
func (i WindowInfo) String() string {
	title := i.Title
	if title == "" {
		title = "(untitled)"
	}
	s := fmt.Sprintf("pid %d %s — %q at %s", i.PID, i.App, title, i.Frame)
	if i.Display != 0 {
		s += fmt.Sprintf(" on display %d", i.Display)
	}
	if i.Minimized {
		s += " [minimized]"
	}
	return s
}

// Annotate fills in each window's Display from a display list. It is separate
// from the listing itself so that the attribution — which is [DisplayFor], and
// is not obvious — is portable policy rather than something buried in a
// platform file.
func Annotate(windows []WindowInfo, displays []Display) []WindowInfo {
	out := make([]WindowInfo, len(windows))
	copy(out, windows)
	for i := range out {
		if d, ok := DisplayFor(out[i].Frame, displays); ok {
			out[i].Display = d.ID
		}
	}
	return out
}

// SortWindows orders a listing the way a person reads one: by application name,
// then by window title, then by position. It is stable across runs, which
// matters because AX returns windows in an order that changes as the user
// clicks around.
func SortWindows(windows []WindowInfo) {
	sort.SliceStable(windows, func(a, b int) bool {
		x, y := windows[a], windows[b]
		if x.App != y.App {
			return x.App < y.App
		}
		if x.Title != y.Title {
			return x.Title < y.Title
		}
		if x.Frame.X != y.Frame.X {
			return x.Frame.X < y.Frame.X
		}
		return x.Frame.Y < y.Frame.Y
	})
}

// ---------------------------------------------------------------------------
// Trust.
// ---------------------------------------------------------------------------

// Trust is what this process may do with the Accessibility API, and why.
//
// The "why" is the part that is worth having. On macOS the TCC grant does not
// attach to the executable that asks for it: it attaches to the RESPONSIBLE
// process, which for a command-line binary is the terminal that launched it and
// for a bundled application is the .app. So an unbundled Go binary is trusted
// exactly when its terminal is, will never appear in System Settings under its
// own name, and cannot be granted the permission on its own. Telling a user to
// "add this binary in System Settings" when that is impossible is worse than
// telling them nothing.
type Trust struct {
	// Trusted is AXIsProcessTrusted(): whether AX will answer for other
	// applications right now.
	Trusted bool
	// Bundled reports whether the running executable is inside a .app
	// bundle.
	Bundled bool
	// Bundle is the bundle identifier, empty for an unbundled binary.
	Bundle string
	// Name is the application's name as System Settings would list it, or
	// the executable's base name for an unbundled binary.
	Name string
	// Path is the executable's path.
	Path string
	// Responsible is the name of the process that actually holds the grant
	// when this one is unbundled — the terminal, usually. It is empty when
	// it could not be determined.
	Responsible string
}

// String renders the trust state in one line.
func (t Trust) String() string {
	state := "NOT trusted"
	if t.Trusted {
		state = "trusted"
	}
	kind := "unbundled binary"
	if t.Bundled {
		kind = "bundled application " + t.Bundle
	}
	return fmt.Sprintf("%s (%s, %s)", state, t.Name, kind)
}

// Advice returns what a person has to do about the current state, in words that
// are true for this process rather than the generic instruction that is wrong
// half the time.
func (t Trust) Advice() string {
	switch {
	case t.Trusted && t.Bundled:
		return fmt.Sprintf("%s is trusted for Accessibility; nothing to do.", t.Name)
	case t.Trusted:
		return fmt.Sprintf("%s is trusted for Accessibility, by way of whatever launched it%s; "+
			"nothing to do, but note that the grant belongs to that parent and not to this binary, "+
			"so running it from somewhere else may not be trusted.", t.Name, respSuffix(t))
	case t.Bundled:
		return fmt.Sprintf("Grant %s Accessibility: System Settings → Privacy & Security → "+
			"Accessibility, then add or switch on %s. Nothing here will work until you do.", t.Name, t.Name)
	default:
		return fmt.Sprintf("%s is a command-line binary, so it cannot be granted Accessibility "+
			"under its own name — macOS gives the grant to whatever launched it%s. "+
			"Switch that on in System Settings → Privacy & Security → Accessibility, "+
			"or ship this inside a .app bundle and grant the bundle.", t.Name, respSuffix(t))
	}
}

// respSuffix names the responsible process when it is known.
func respSuffix(t Trust) string {
	if t.Responsible == "" {
		return " (your terminal, if you started it from a shell)"
	}
	return " (" + t.Responsible + ")"
}

// ---------------------------------------------------------------------------
// AXError.
// ---------------------------------------------------------------------------

// AXError is a status value from the Accessibility API. It is defined here,
// in the portable half, because it is pure data: the mapping from a number to
// what it means to a caller is the part that can be wrong, and it is tested
// everywhere rather than only on a Mac.
//
// Note what an AXError CANNOT tell you. A write to kAXPositionAttribute that
// the application quietly ignores returns [AXSuccess]. That is the whole reason
// [Move] measures instead of trusting a status.
type AXError int32

// The AXError values from HIServices/AXError.h.
const (
	AXSuccess                    AXError = 0
	AXFailure                    AXError = -25200
	AXIllegalArgument            AXError = -25201
	AXInvalidUIElement           AXError = -25202
	AXInvalidUIElementObserver   AXError = -25203
	AXCannotComplete             AXError = -25204
	AXAttributeUnsupported       AXError = -25205
	AXActionUnsupported          AXError = -25206
	AXNotificationUnsupported    AXError = -25207
	AXNotImplemented             AXError = -25208
	AXNotificationAlreadyRegd    AXError = -25209
	AXNotificationNotRegistered  AXError = -25210
	AXAPIDisabled                AXError = -25211
	AXNoValue                    AXError = -25212
	AXParameterizedAttrUnsupport AXError = -25213
	AXNotEnoughPrecision         AXError = -25214
)

// axErrorNames explains each status in the terms a caller of THIS package needs,
// not in the terms of the header.
var axErrorNames = map[AXError]string{
	AXSuccess:                    "success",
	AXFailure:                    "a general failure",
	AXIllegalArgument:            "an illegal argument",
	AXInvalidUIElement:           "the element is no longer valid (the window was probably closed)",
	AXInvalidUIElementObserver:   "the observer is not valid",
	AXCannotComplete:             "the application did not answer (it may be busy, hung, or not accessibility-aware)",
	AXAttributeUnsupported:       "the element does not have that attribute",
	AXActionUnsupported:          "the element does not support that action",
	AXNotificationUnsupported:    "the element does not support that notification",
	AXNotImplemented:             "the application does not implement the accessibility API",
	AXNotificationAlreadyRegd:    "already registered for that notification",
	AXNotificationNotRegistered:  "not registered for that notification",
	AXAPIDisabled:                "the accessibility API is disabled for this process",
	AXNoValue:                    "the attribute has no value",
	AXParameterizedAttrUnsupport: "the element does not have that parameterized attribute",
	AXNotEnoughPrecision:         "not enough precision",
}

// Error implements error.
func (e AXError) Error() string {
	if s, ok := axErrorNames[e]; ok {
		return fmt.Sprintf("AXError %d: %s", int32(e), s)
	}
	return fmt.Sprintf("AXError %d", int32(e))
}

// Err turns a status into an error: nil for [AXSuccess], and an error that
// wraps [ErrNotTrusted] for [AXAPIDisabled], so a caller can tell a permission
// problem — which a person can fix — from a window that has gone away, which
// they cannot.
func (e AXError) Err(op string) error {
	switch e {
	case AXSuccess:
		return nil
	case AXAPIDisabled:
		return fmt.Errorf("accessibility: %s: %w (%s)", op, ErrNotTrusted, e)
	default:
		return fmt.Errorf("accessibility: %s: %w", op, e)
	}
}

// offBy is how far a window is from where it was told to go: the larger of the
// two axes, which is the quantity a write has to reduce to be making progress.
func offBy(got, want Rect) float64 {
	return math.Max(math.Abs(got.X-want.X), math.Abs(got.Y-want.Y))
}
