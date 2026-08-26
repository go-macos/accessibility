package accessibility

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// A window that does exactly what a test tells it to.
// ---------------------------------------------------------------------------

// fakeWindow is the whole reason [Window] is an interface. It can do the two
// things a real window does that no status code reveals: accept a write and
// ignore it, and accept a position and then move itself somewhere else when the
// size is set.
type fakeWindow struct {
	frame Rect

	// pin makes every write a no-op — a window that answers "yes" and does
	// not move. This is the failure the whole package exists to catch.
	pin bool
	// pinSize refuses size changes only, which is what a window with a
	// minimum size really does.
	pinSize bool
	// nudge is added to the origin when the size is set, standing in for
	// the window server pushing a window back inside a display.
	nudge Point
	// nudgeOnce applies the nudge on the first size write only, so the
	// second attempt succeeds — the ordinary two-attempt case.
	nudgeOnce bool

	frameErr    error
	frameErrAt  int // fail on the Nth Frame call (1-based); 0 means every call
	frames      int
	posErr      error
	sizeErr     error
	raiseErr    error
	raised      int
	positionSet int
	sizeSet     int
	sizesWanted []Size
}

func (f *fakeWindow) Frame() (Rect, error) {
	f.frames++
	if f.frameErr != nil && (f.frameErrAt == 0 || f.frameErrAt == f.frames) {
		return Rect{}, f.frameErr
	}
	return f.frame, nil
}

func (f *fakeWindow) SetPosition(p Point) error {
	if f.posErr != nil {
		return f.posErr
	}
	f.positionSet++
	if !f.pin {
		f.frame.X, f.frame.Y = p.X, p.Y
	}
	return nil
}

func (f *fakeWindow) SetSize(s Size) error {
	if f.sizeErr != nil {
		return f.sizeErr
	}
	f.sizeSet++
	f.sizesWanted = append(f.sizesWanted, s)
	if !f.pin && !f.pinSize {
		f.frame.W, f.frame.H = s.W, s.H
	}
	if (f.nudge != Point{}) && (!f.nudgeOnce || f.sizeSet == 1) {
		f.frame.X += f.nudge.X
		f.frame.Y += f.nudge.Y
	}
	return nil
}

func (f *fakeWindow) Raise() error {
	if f.raiseErr != nil {
		return f.raiseErr
	}
	f.raised++
	return nil
}

// ---------------------------------------------------------------------------
// Geometry.
// ---------------------------------------------------------------------------

func TestRectBasics(t *testing.T) {
	r := Rect{10, 20, 100, 50}
	if got := r.Origin(); got != (Point{10, 20}) {
		t.Errorf("Origin = %v", got)
	}
	if got := r.Size(); got != (Size{100, 50}) {
		t.Errorf("Size = %v", got)
	}
	if r.Right() != 110 || r.Bottom() != 70 {
		t.Errorf("Right/Bottom = %g/%g", r.Right(), r.Bottom())
	}
	if got := r.Center(); got != (Point{60, 45}) {
		t.Errorf("Center = %v", got)
	}
	if r.Empty() {
		t.Error("Empty on a non-empty rectangle")
	}
	if r.Area() != 5000 {
		t.Errorf("Area = %g", r.Area())
	}
	if got := r.Offset(5, -5); got != (Rect{15, 15, 100, 50}) {
		t.Errorf("Offset = %v", got)
	}
	if got := r.String(); got != "10,20 100x50" {
		t.Errorf("String = %q", got)
	}
	if got := r.Origin().String(); got != "(10,20)" {
		t.Errorf("Point.String = %q", got)
	}
	if got := r.Size().String(); got != "100x50" {
		t.Errorf("Size.String = %q", got)
	}
}

func TestRectEmptyAndArea(t *testing.T) {
	for _, r := range []Rect{{0, 0, 0, 10}, {0, 0, 10, 0}, {0, 0, -1, 10}} {
		if !r.Empty() {
			t.Errorf("%v: Empty = false", r)
		}
		if r.Area() != 0 {
			t.Errorf("%v: Area = %g, want 0", r, r.Area())
		}
	}
}

func TestRectContains(t *testing.T) {
	r := Rect{0, 0, 100, 100}
	// The top-left edges belong to the rectangle, the bottom-right ones do
	// not, so two adjacent displays never both claim the same point.
	for _, tc := range []struct {
		p    Point
		want bool
	}{
		{Point{0, 0}, true}, {Point{99, 99}, true},
		{Point{100, 50}, false}, {Point{50, 100}, false},
		{Point{-1, 50}, false}, {Point{50, -1}, false},
	} {
		if got := r.Contains(tc.p); got != tc.want {
			t.Errorf("Contains(%v) = %v, want %v", tc.p, got, tc.want)
		}
	}
}

func TestRectIntersect(t *testing.T) {
	a := Rect{0, 0, 100, 100}
	if got := a.Intersect(Rect{50, 50, 100, 100}); got != (Rect{50, 50, 50, 50}) {
		t.Errorf("overlap = %v", got)
	}
	if got := a.Intersect(Rect{200, 0, 10, 10}); got != (Rect{}) {
		t.Errorf("disjoint on x = %v, want zero", got)
	}
	if got := a.Intersect(Rect{0, 200, 10, 10}); got != (Rect{}) {
		t.Errorf("disjoint on y = %v, want zero", got)
	}
	if got := a.Intersect(Rect{100, 0, 10, 10}); got != (Rect{}) {
		t.Errorf("edge-to-edge = %v, want zero", got)
	}
}

func TestRectInset(t *testing.T) {
	r := Rect{0, 0, 100, 100}
	if got := r.Inset(0); got != r {
		t.Errorf("Inset(0) = %v, want unchanged", got)
	}
	if got := r.Inset(10); got != (Rect{10, 10, 80, 80}) {
		t.Errorf("Inset(10) = %v", got)
	}
	// An inset that would leave nothing is refused rather than obeyed: an
	// empty target display is never what a caller means.
	if got := r.Inset(50); got != r {
		t.Errorf("Inset(50) = %v, want unchanged", got)
	}
	if got := r.Inset(500); got != r {
		t.Errorf("Inset(500) = %v, want unchanged", got)
	}
}

func TestRectNearlyEqual(t *testing.T) {
	r := Rect{100, 100, 200, 200}
	for _, tc := range []struct {
		o    Rect
		tol  float64
		want bool
	}{
		{Rect{100, 100, 200, 200}, 0, true},
		{Rect{101, 100, 200, 200}, 2, true},
		{Rect{100, 101, 200, 200}, 2, true},
		{Rect{100, 100, 201, 200}, 2, true},
		{Rect{100, 100, 200, 201}, 2, true},
		{Rect{104, 100, 200, 200}, 2, false},
		{Rect{100, 104, 200, 200}, 2, false},
		{Rect{100, 100, 204, 200}, 2, false},
		{Rect{100, 100, 200, 204}, 2, false},
	} {
		if got := r.NearlyEqual(tc.o, tc.tol); got != tc.want {
			t.Errorf("NearlyEqual(%v, %g) = %v, want %v", tc.o, tc.tol, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Displays.
// ---------------------------------------------------------------------------

// ribbon is a three-display arrangement of the kind go-xrkit/desk builds: a
// main display at the origin, one to its right, and one ABOVE it, so negative
// coordinates are in play. Real machines produce these; a test that only ever
// uses positive coordinates misses a whole class of sign error.
var ribbon = []Display{
	{ID: 2, Bounds: Rect{0, 0, 1920, 1200}, Main: true},
	{ID: 5, Bounds: Rect{1920, 0, 1920, 1200}},
	{ID: 4, Bounds: Rect{0, -2160, 3840, 2160}},
}

func TestDisplayString(t *testing.T) {
	if got := ribbon[0].String(); got != "display 2 [0,0 1920x1200] (main)" {
		t.Errorf("main display String = %q", got)
	}
	if got := ribbon[1].String(); got != "display 5 [1920,0 1920x1200]" {
		t.Errorf("secondary display String = %q", got)
	}
}

func TestDisplayByID(t *testing.T) {
	if d, ok := DisplayByID(ribbon, 5); !ok || d.Bounds.X != 1920 {
		t.Errorf("DisplayByID(5) = %v, %v", d, ok)
	}
	if _, ok := DisplayByID(ribbon, 99); ok {
		t.Error("DisplayByID(99) found something")
	}
}

func TestMainDisplay(t *testing.T) {
	if d, ok := MainDisplay(ribbon); !ok || d.ID != 2 {
		t.Errorf("MainDisplay = %v, %v", d, ok)
	}
	if _, ok := MainDisplay([]Display{{ID: 7}}); ok {
		t.Error("MainDisplay found a main display where there is none")
	}
}

func TestDisplayForEmpty(t *testing.T) {
	if _, ok := DisplayFor(Rect{0, 0, 10, 10}, nil); ok {
		t.Error("DisplayFor with no displays reported one")
	}
}

func TestDisplayForOverlap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame Rect
		want  uint32
	}{
		{"wholly on the main display", Rect{100, 100, 800, 600}, 2},
		{"wholly on the right-hand one", Rect{2000, 100, 800, 600}, 5},
		{"wholly on the one above", Rect{100, -2000, 800, 600}, 4},
		// The origin is on display 2 and only a sliver of the window is;
		// most of it is on display 5. Answering "2" here — which is what
		// an origin test does — would make MoveToDisplay compute a
		// relative position against the wrong display.
		{"straddling, mostly right", Rect{1900, 100, 800, 600}, 5},
		{"straddling, mostly main", Rect{1200, 100, 800, 600}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := DisplayFor(tc.frame, ribbon)
			if !ok || d.ID != tc.want {
				t.Errorf("DisplayFor(%v) = %v (ok=%v), want display %d", tc.frame, d, ok, tc.want)
			}
		})
	}
}

func TestDisplayForTieGoesToTheLowestID(t *testing.T) {
	// Two displays covering exactly the same area: the answer must not
	// depend on the order the display list came back in.
	twins := []Display{{ID: 9, Bounds: Rect{0, 0, 100, 100}}, {ID: 3, Bounds: Rect{0, 0, 100, 100}}}
	d, ok := DisplayFor(Rect{0, 0, 50, 50}, twins)
	if !ok || d.ID != 3 {
		t.Errorf("tie = %v, want display 3", d)
	}
	d, _ = DisplayFor(Rect{0, 0, 50, 50}, []Display{twins[1], twins[0]})
	if d.ID != 3 {
		t.Errorf("tie the other way round = %v, want display 3", d)
	}
}

func TestDisplayForOffEveryDisplay(t *testing.T) {
	// A window left behind by an unplugged display overlaps nothing at
	// all. It still has to be attributable to something, or it can never be
	// brought back.
	d, ok := DisplayFor(Rect{9000, 9000, 100, 100}, ribbon)
	if !ok {
		t.Fatal("DisplayFor gave up on an off-screen window")
	}
	if d.ID != 5 {
		t.Errorf("nearest centre = display %d, want 5 (the right-hand one)", d.ID)
	}
}

func TestDisplayForOffScreenTieGoesToTheLowestID(t *testing.T) {
	twins := []Display{
		{ID: 9, Bounds: Rect{-100, 0, 100, 100}},
		{ID: 3, Bounds: Rect{100, 0, 100, 100}},
	}
	// Equidistant from both centres, and overlapping neither.
	d, ok := DisplayFor(Rect{45, 500, 10, 10}, twins)
	if !ok || d.ID != 3 {
		t.Errorf("equidistant tie = %v (ok=%v), want display 3", d, ok)
	}
}

// ---------------------------------------------------------------------------
// Options and Placement.
// ---------------------------------------------------------------------------

func TestPlacementString(t *testing.T) {
	for p, want := range map[Placement]string{
		Relative: "relative", Origin: "origin", Center: "center", Fill: "fill",
		Placement(42): "placement(42)",
	} {
		if got := p.String(); got != want {
			t.Errorf("Placement(%d).String() = %q, want %q", int(p), got, want)
		}
	}
}

func TestNilOptionsAreTheDefaults(t *testing.T) {
	var o *Options
	if o.placement() != Relative {
		t.Error("nil Options placement is not Relative")
	}
	if o.inset() != 0 {
		t.Error("nil Options inset is not 0")
	}
	if !o.clamp() {
		t.Error("nil Options does not clamp")
	}
	if !o.raise() {
		t.Error("nil Options does not raise")
	}
	if o.tolerance() != DefaultTolerance {
		t.Errorf("nil Options tolerance = %g", o.tolerance())
	}
}

func TestOptionsAccessors(t *testing.T) {
	o := &Options{Placement: Fill, Inset: 12, NoClamp: true, NoRaise: true, Tolerance: 7}
	if o.placement() != Fill || o.inset() != 12 || o.clamp() || o.raise() || o.tolerance() != 7 {
		t.Errorf("accessors disagree with %+v", o)
	}
	// A negative inset would grow the target beyond the display.
	if got := (&Options{Inset: -5}).inset(); got != 0 {
		t.Errorf("negative inset = %g, want 0", got)
	}
	// A negative tolerance means exact, not "anything goes".
	if got := (&Options{Tolerance: -1}).tolerance(); got != 0 {
		t.Errorf("negative tolerance = %g, want 0", got)
	}
	if got := (&Options{}).tolerance(); got != DefaultTolerance {
		t.Errorf("zero tolerance = %g, want the default", got)
	}
}

// ---------------------------------------------------------------------------
// Place: the geometry policy.
// ---------------------------------------------------------------------------

func TestPlaceRelativeKeepsPositionAndProportion(t *testing.T) {
	from, to := ribbon[0], ribbon[2] // 1920x1200 at (0,0) → 3840x2160 at (0,-2160)
	// The left half, full height, of the main display.
	got := Place(Rect{0, 0, 960, 1200}, from, to, nil)
	want := Rect{0, -2160, 1920, 2160}
	if !got.NearlyEqual(want, 0.001) {
		t.Errorf("Place = %v, want %v", got, want)
	}
	// A quarter-size window at the centre stays a quarter-size window at
	// the centre.
	got = Place(Rect{720, 450, 480, 300}, from, to, nil)
	want = Rect{1440, -2160 + 810, 960, 540}
	if !got.NearlyEqual(want, 0.001) {
		t.Errorf("Place (centred) = %v, want %v", got, want)
	}
}

func TestPlaceOriginCenterFill(t *testing.T) {
	from, to := ribbon[0], ribbon[1] // → 1920x1200 at (1920,0)
	frame := Rect{100, 100, 800, 600}
	for _, tc := range []struct {
		p    Placement
		want Rect
	}{
		{Origin, Rect{1920, 0, 800, 600}},
		{Center, Rect{1920 + 560, 300, 800, 600}},
		{Fill, Rect{1920, 0, 1920, 1200}},
	} {
		if got := Place(frame, from, to, &Options{Placement: tc.p}); !got.NearlyEqual(tc.want, 0.001) {
			t.Errorf("Place(%s) = %v, want %v", tc.p, got, tc.want)
		}
	}
}

func TestPlaceUnknownPlacementFallsBackToOrigin(t *testing.T) {
	got := Place(Rect{100, 100, 800, 600}, ribbon[0], ribbon[1], &Options{Placement: Placement(99)})
	if want := (Rect{1920, 0, 800, 600}); !got.NearlyEqual(want, 0.001) {
		t.Errorf("unknown placement = %v, want %v", got, want)
	}
}

func TestPlaceRelativeFromADisplayWithNoExtent(t *testing.T) {
	// This is what an unplugged display leaves behind. Dividing by its
	// width would give NaN, and a NaN position is accepted by AX and puts
	// the window nowhere at all.
	gone := Display{ID: 77, Bounds: Rect{0, 0, 0, 0}}
	got := Place(Rect{100, 100, 800, 600}, gone, ribbon[1], nil)
	if want := (Rect{1920, 0, 800, 600}); !got.NearlyEqual(want, 0.001) {
		t.Errorf("Place from a dead display = %v, want %v", got, want)
	}
	if math.IsNaN(got.X) || math.IsNaN(got.Y) || math.IsNaN(got.W) || math.IsNaN(got.H) {
		t.Errorf("Place produced NaN: %v", got)
	}
}

func TestPlaceInset(t *testing.T) {
	got := Place(Rect{0, 0, 100, 100}, ribbon[0], ribbon[1], &Options{Placement: Fill, Inset: 20})
	if want := (Rect{1940, 20, 1880, 1160}); !got.NearlyEqual(want, 0.001) {
		t.Errorf("Place(Fill, inset 20) = %v, want %v", got, want)
	}
}

func TestPlaceClamps(t *testing.T) {
	to := ribbon[1] // 1920x1200 at (1920,0)
	// Too big for the display, and pinned to its origin.
	got := Place(Rect{0, 0, 4000, 3000}, Display{ID: 1, Bounds: Rect{0, 0, 4000, 3000}}, to, nil)
	if want := (Rect{1920, 0, 1920, 1200}); !got.NearlyEqual(want, 0.001) {
		t.Errorf("oversized clamp = %v, want %v", got, want)
	}
	// Hanging off the right-hand edge: pulled back, not shrunk.
	got = Place(Rect{3400, 0, 800, 600}, to, to, &Options{Placement: Origin})
	_ = got
	got = clampTo(Rect{3500, 900, 800, 600}, to.Bounds)
	if want := (Rect{3040, 600, 800, 600}); !got.NearlyEqual(want, 0.001) {
		t.Errorf("edge clamp = %v, want %v", got, want)
	}
	// Hanging off the LEFT/TOP edge: pushed in.
	got = clampTo(Rect{1000, -400, 800, 600}, to.Bounds)
	if want := (Rect{1920, 0, 800, 600}); !got.NearlyEqual(want, 0.001) {
		t.Errorf("near-edge clamp = %v, want %v", got, want)
	}
}

func TestPlaceNoClampLeavesItHangingOff(t *testing.T) {
	to := ribbon[1]
	got := Place(Rect{0, 0, 4000, 3000}, ribbon[0], to,
		&Options{Placement: Origin, NoClamp: true})
	if want := (Rect{1920, 0, 4000, 3000}); !got.NearlyEqual(want, 0.001) {
		t.Errorf("NoClamp = %v, want the oversized %v left alone", got, want)
	}
}

func TestClampToADisplayWithNoExtent(t *testing.T) {
	// Nothing sensible can be clamped into nothing, so the rectangle comes
	// back untouched rather than collapsed to a point.
	r := Rect{10, 20, 30, 40}
	if got := clampTo(r, Rect{}); got != r {
		t.Errorf("clampTo(empty) = %v, want unchanged", got)
	}
}

// ---------------------------------------------------------------------------
// Move: the read-back check, which is the point of the package.
// ---------------------------------------------------------------------------

func TestMoveNilWindow(t *testing.T) {
	if _, err := Move(nil, Rect{}, nil); err == nil {
		t.Fatal("Move(nil) returned no error")
	}
	if _, err := MoveToDisplay(nil, ribbon[0], ribbon, nil); err == nil {
		t.Fatal("MoveToDisplay(nil) returned no error")
	}
}

func TestMoveHappyPath(t *testing.T) {
	w := &fakeWindow{frame: Rect{100, 100, 800, 600}}
	res, err := Move(w, Rect{1920, 0, 1024, 768}, nil)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if !res.Moved || !res.Resized || !res.Raised {
		t.Errorf("Result = %+v", res)
	}
	if res.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", res.Attempts)
	}
	if res.Before != (Rect{100, 100, 800, 600}) || res.Got != (Rect{1920, 0, 1024, 768}) {
		t.Errorf("Before/Got = %v / %v", res.Before, res.Got)
	}
	if w.raised != 1 {
		t.Errorf("raised %d times, want 1", w.raised)
	}
}

// THE NEGATIVE CONTROL, in portable form. A window that accepts every write and
// does not move is exactly what AX hands you for a pinned or full-screen
// window, and it is invisible to a status check.
func TestMoveCatchesAWindowThatDoesNotMove(t *testing.T) {
	w := &fakeWindow{frame: Rect{100, 100, 800, 600}, pin: true}
	res, err := Move(w, Rect{1920, 0, 1024, 768}, nil)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("Move returned %v, want ErrRefused", err)
	}
	if res.Moved {
		t.Error("Result.Moved is true for a window that never moved")
	}
	if res.Got != res.Before {
		t.Errorf("Got = %v, want the unchanged %v", res.Got, res.Before)
	}
	if w.positionSet != defaultAttempts {
		t.Errorf("gave up after %d position writes, want %d", w.positionSet, defaultAttempts)
	}
	// A refused move must NOT raise: bringing a window forward that is
	// still on the wrong display is worse than leaving it alone.
	if w.raised != 0 {
		t.Error("a refused move raised the window anyway")
	}
	if !strings.Contains(err.Error(), "NOT MOVED") {
		t.Errorf("the error does not say what happened: %v", err)
	}
}

// A size the window refuses is NOT a failed move. This is the common case: a
// terminal that snaps to whole character cells, a window with a minimum size.
func TestMoveSucceedsWhenOnlyTheSizeIsRefused(t *testing.T) {
	w := &fakeWindow{frame: Rect{100, 100, 800, 600}, pinSize: true}
	res, err := Move(w, Rect{1920, 0, 200, 150}, nil)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if !res.Moved || res.Resized {
		t.Errorf("Result = %+v, want Moved && !Resized", res)
	}
	if res.Attempts != defaultAttempts {
		t.Errorf("Attempts = %d, want %d (it retries while the size is wrong)", res.Attempts, defaultAttempts)
	}
	if !strings.Contains(res.String(), "size refused") {
		t.Errorf("Result.String does not mention the refused size: %q", res.String())
	}
}

// Setting the size can push the origin back — the window server refusing to
// leave a newly enlarged window hanging off an edge. That is why the position
// is asserted a second time.
func TestMoveReassertsThePositionAfterAResize(t *testing.T) {
	w := &fakeWindow{frame: Rect{100, 100, 800, 600}, nudge: Point{40, 40}, nudgeOnce: true}
	res, err := Move(w, Rect{1920, 0, 1024, 768}, nil)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if res.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", res.Attempts)
	}
	if !res.Moved || !res.Resized {
		t.Errorf("Result = %+v", res)
	}
	if !strings.Contains(res.String(), "after 2 attempts") {
		t.Errorf("Result.String does not report the retry: %q", res.String())
	}
}

func TestMoveDoesNotResizeAWindowThatIsAlreadyTheRightSize(t *testing.T) {
	w := &fakeWindow{frame: Rect{100, 100, 800, 600}}
	if _, err := Move(w, Rect{1920, 0, 800, 600}, nil); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if w.sizeSet != 0 {
		t.Errorf("wrote the size %d times for a window that was already that size", w.sizeSet)
	}
}

func TestMoveNoRaise(t *testing.T) {
	w := &fakeWindow{frame: Rect{100, 100, 800, 600}}
	res, err := Move(w, Rect{1920, 0, 800, 600}, &Options{NoRaise: true})
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if res.Raised || w.raised != 0 {
		t.Error("NoRaise raised the window anyway")
	}
}

func TestMoveErrorPaths(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name string
		w    *fakeWindow
		want string
	}{
		{"reading before", &fakeWindow{frameErr: boom}, "before moving it"},
		{"setting the position", &fakeWindow{posErr: boom}, "setting the position"},
		{"setting the size", &fakeWindow{sizeErr: boom}, "setting the size"},
		{"reading back", &fakeWindow{frameErr: boom, frameErrAt: 2}, "after moving it"},
		{"raising", &fakeWindow{raiseErr: boom}, "raising the window"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Move(tc.w, Rect{1920, 0, 1024, 768}, nil)
			if err == nil {
				t.Fatal("no error")
			}
			if !errors.Is(err, boom) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one wrapping boom and mentioning %q", err, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MoveToDisplay.
// ---------------------------------------------------------------------------

func TestMoveToDisplayNoDisplays(t *testing.T) {
	w := &fakeWindow{frame: Rect{0, 0, 10, 10}}
	if _, err := MoveToDisplay(w, ribbon[0], nil, nil); !errors.Is(err, ErrNoDisplays) {
		t.Fatalf("err = %v, want ErrNoDisplays", err)
	}
}

func TestMoveToDisplayFrameError(t *testing.T) {
	boom := errors.New("boom")
	if _, err := MoveToDisplay(&fakeWindow{frameErr: boom}, ribbon[0], ribbon, nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want one wrapping boom", err)
	}
}

func TestMoveToDisplayCarriesTheRibbon(t *testing.T) {
	// A window filling the left half of the main display goes to the left
	// half of the display above it — this is "ribbon position 3" in the
	// consumer's terms.
	w := &fakeWindow{frame: Rect{0, 0, 960, 1200}}
	res, err := MoveToDisplay(w, ribbon[2], ribbon, nil)
	if err != nil {
		t.Fatalf("MoveToDisplay: %v", err)
	}
	if res.From.ID != 2 || res.To.ID != 4 {
		t.Errorf("From/To = %d/%d, want 2/4", res.From.ID, res.To.ID)
	}
	if want := (Rect{0, -2160, 1920, 2160}); !res.Got.NearlyEqual(want, 0.001) {
		t.Errorf("Got = %v, want %v", res.Got, want)
	}
}

func TestMoveToDisplayReportsTheDisplaysEvenWhenRefused(t *testing.T) {
	w := &fakeWindow{frame: Rect{0, 0, 960, 1200}, pin: true}
	res, err := MoveToDisplay(w, ribbon[1], ribbon, nil)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
	if res.From.ID != 2 || res.To.ID != 5 {
		t.Errorf("From/To = %d/%d, want 2/5 — a failure must still say where it was trying to go",
			res.From.ID, res.To.ID)
	}
}

// ---------------------------------------------------------------------------
// Result formatting.
// ---------------------------------------------------------------------------

func TestResultString(t *testing.T) {
	r := Result{Before: Rect{0, 0, 100, 100}, Wanted: Rect{200, 0, 100, 100},
		Got: Rect{200, 0, 100, 100}, Moved: true, Resized: true, Attempts: 1}
	if got := r.String(); got != "0,0 100x100 → wanted 200,0 100x100, got 200,0 100x100" {
		t.Errorf("String = %q", got)
	}
}

// ---------------------------------------------------------------------------
// Listings.
// ---------------------------------------------------------------------------

func TestWindowInfoString(t *testing.T) {
	for _, tc := range []struct {
		i    WindowInfo
		want string
	}{
		{WindowInfo{PID: 1, App: "Finder", Title: "Downloads", Frame: Rect{0, 0, 10, 10}},
			`pid 1 Finder — "Downloads" at 0,0 10x10`},
		{WindowInfo{PID: 2, App: "TextEdit", Frame: Rect{0, 0, 10, 10}, Display: 4},
			`pid 2 TextEdit — "(untitled)" at 0,0 10x10 on display 4`},
		{WindowInfo{PID: 3, App: "Mail", Title: "Inbox", Frame: Rect{0, 0, 10, 10}, Minimized: true},
			`pid 3 Mail — "Inbox" at 0,0 10x10 [minimized]`},
	} {
		if got := tc.i.String(); got != tc.want {
			t.Errorf("String = %q, want %q", got, tc.want)
		}
	}
}

func TestAnnotate(t *testing.T) {
	in := []WindowInfo{
		{PID: 1, Frame: Rect{100, 100, 200, 200}},
		{PID: 2, Frame: Rect{2000, 100, 200, 200}},
		{PID: 3, Frame: Rect{100, -2000, 200, 200}},
	}
	out := Annotate(in, ribbon)
	for i, want := range []uint32{2, 5, 4} {
		if out[i].Display != want {
			t.Errorf("window %d attributed to display %d, want %d", i, out[i].Display, want)
		}
	}
	// The input must not be modified: a caller may well be holding it.
	for i := range in {
		if in[i].Display != 0 {
			t.Errorf("Annotate modified its input at %d", i)
		}
	}
	// With no displays, nothing is attributed and nothing blows up.
	if got := Annotate(in, nil); got[0].Display != 0 {
		t.Errorf("Annotate with no displays set a display: %v", got[0])
	}
}

func TestSortWindows(t *testing.T) {
	in := []WindowInfo{
		{App: "Safari", Title: "b", Frame: Rect{0, 0, 1, 1}},
		{App: "Finder", Title: "z", Frame: Rect{0, 0, 1, 1}},
		{App: "Finder", Title: "a", Frame: Rect{50, 0, 1, 1}},
		{App: "Finder", Title: "a", Frame: Rect{10, 0, 1, 1}},
		{App: "Finder", Title: "a", Frame: Rect{10, 99, 1, 1}},
	}
	SortWindows(in)
	var got []string
	for _, w := range in {
		got = append(got, fmt.Sprintf("%s/%s/%g,%g", w.App, w.Title, w.Frame.X, w.Frame.Y))
	}
	want := []string{"Finder/a/10,0", "Finder/a/10,99", "Finder/a/50,0", "Finder/z/0,0", "Safari/b/0,0"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("sorted = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Trust.
// ---------------------------------------------------------------------------

func TestTrustString(t *testing.T) {
	for _, tc := range []struct {
		t    Trust
		want string
	}{
		{Trust{Trusted: true, Name: "desk", Bundled: true, Bundle: "io.xrkit.desk"},
			"trusted (desk, bundled application io.xrkit.desk)"},
		{Trust{Name: "axmove"}, "NOT trusted (axmove, unbundled binary)"},
	} {
		if got := tc.t.String(); got != tc.want {
			t.Errorf("String = %q, want %q", got, tc.want)
		}
	}
}

func TestTrustAdvice(t *testing.T) {
	// The four states have to say four different things. The one that
	// matters most is the last: telling a user to add a command-line binary
	// in System Settings is advice they cannot follow.
	for _, tc := range []struct {
		name     string
		t        Trust
		contains string
		absent   string
	}{
		{"bundled and trusted",
			Trust{Trusted: true, Bundled: true, Name: "desk"}, "nothing to do", "System Settings"},
		{"unbundled and trusted",
			Trust{Trusted: true, Name: "axmove", Responsible: "Terminal"},
			"the grant belongs to that parent", ""},
		{"bundled, not trusted",
			Trust{Bundled: true, Name: "desk"}, "System Settings", "cannot be granted"},
		{"unbundled, not trusted",
			Trust{Name: "axmove", Responsible: "iTerm2"},
			"cannot be granted Accessibility under its own name", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.t.Advice()
			if !strings.Contains(got, tc.contains) {
				t.Errorf("Advice = %q, want it to mention %q", got, tc.contains)
			}
			if tc.absent != "" && strings.Contains(got, tc.absent) {
				t.Errorf("Advice = %q, must not mention %q", got, tc.absent)
			}
		})
	}
}

func TestTrustAdviceNamesTheResponsibleProcessWhenItIsKnown(t *testing.T) {
	with := Trust{Name: "axmove", Responsible: "Ghostty"}.Advice()
	if !strings.Contains(with, "(Ghostty)") {
		t.Errorf("Advice = %q, want it to name Ghostty", with)
	}
	without := Trust{Name: "axmove"}.Advice()
	if !strings.Contains(without, "your terminal, if you started it from a shell") {
		t.Errorf("Advice with no responsible process = %q", without)
	}
}

// ---------------------------------------------------------------------------
// AXError.
// ---------------------------------------------------------------------------

func TestAXErrorMessages(t *testing.T) {
	if got := AXCannotComplete.Error(); !strings.Contains(got, "-25204") ||
		!strings.Contains(got, "did not answer") {
		t.Errorf("AXCannotComplete = %q", got)
	}
	// Every constant must have an explanation; a bare number in front of a
	// user is not an explanation.
	for _, e := range []AXError{
		AXSuccess, AXFailure, AXIllegalArgument, AXInvalidUIElement,
		AXInvalidUIElementObserver, AXCannotComplete, AXAttributeUnsupported,
		AXActionUnsupported, AXNotificationUnsupported, AXNotImplemented,
		AXNotificationAlreadyRegd, AXNotificationNotRegistered, AXAPIDisabled,
		AXNoValue, AXParameterizedAttrUnsupport, AXNotEnoughPrecision,
	} {
		if _, ok := axErrorNames[e]; !ok {
			t.Errorf("AXError %d has no explanation", int32(e))
		}
	}
	if got := AXError(-1).Error(); got != "AXError -1" {
		t.Errorf("unknown AXError = %q", got)
	}
}

func TestAXErrorErr(t *testing.T) {
	if err := AXSuccess.Err("reading"); err != nil {
		t.Errorf("AXSuccess.Err = %v, want nil", err)
	}
	// A permission problem a person can fix must be distinguishable from a
	// window that has gone away, which they cannot.
	err := AXAPIDisabled.Err("reading kAXPositionAttribute")
	if !errors.Is(err, ErrNotTrusted) {
		t.Errorf("AXAPIDisabled.Err = %v, want it to wrap ErrNotTrusted", err)
	}
	if !strings.Contains(err.Error(), "reading kAXPositionAttribute") {
		t.Errorf("AXAPIDisabled.Err = %v, want it to name the operation", err)
	}
	err = AXInvalidUIElement.Err("setting kAXSizeAttribute")
	if errors.Is(err, ErrNotTrusted) {
		t.Errorf("AXInvalidUIElement.Err wrongly reports a permission problem: %v", err)
	}
	if !errors.Is(err, AXInvalidUIElement) {
		t.Errorf("AXInvalidUIElement.Err = %v, want errors.Is to find the status", err)
	}
}

// ---------------------------------------------------------------------------
// The seam holds.
// ---------------------------------------------------------------------------

// The darwin *AXWindow must satisfy [Window]; so must the non-darwin stub, so
// that a consumer's own code compiles unchanged on both.
var _ Window = (*AXWindow)(nil)
var _ Window = (*fakeWindow)(nil)
