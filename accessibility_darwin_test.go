//go:build darwin

package accessibility

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// These run on any Mac, in CI included, and none of them needs the
// Accessibility grant: the display list, the application list and the window
// server's own view of the windows are all ungated. What they establish is that
// the purego bindings resolve and that the three coordinate spaces agree — the
// groundwork the live suite then builds a proof on.
//
// Nothing here ever calls [RequestTrust]. A test that pops a system dialog on a
// shared runner would be a bug, and on a developer's machine it would be rude.

func TestLoadResolvesEverySymbol(t *testing.T) {
	if err := load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	for name, fn := range map[string]any{
		"AXIsProcessTrusted":                     axIsProcessTrusted,
		"AXIsProcessTrustedWithOptions":          axIsProcessTrustedWithOptions,
		"AXUIElementCreateApplication":           axUIElementCreateApplication,
		"AXUIElementCopyAttributeValue":          axUIElementCopyAttributeValue,
		"AXUIElementSetAttributeValue":           axUIElementSetAttributeValue,
		"AXUIElementPerformAction":               axUIElementPerformAction,
		"AXValueCreate":                          axValueCreate,
		"AXValueGetValue":                        axValueGetValue,
		"CGWindowListCopyWindowInfo":             cgWindowListCopyWindowInfo,
		"CGRectMakeWithDictionaryRepresentation": cgRectFromDictionary,
		"CGGetActiveDisplayList":                 cgGetActiveDisplayList,
		"CGDisplayBounds":                        cgDisplayBounds,
		"CGMainDisplayID":                        cgMainDisplayID,
	} {
		if fn == nil {
			t.Errorf("%s did not resolve", name)
		}
	}
	for name, s := range map[string]uintptr{
		"kAXWindowsAttribute": strAXWindows, "kAXPositionAttribute": strAXPosition,
		"kAXSizeAttribute": strAXSize, "kAXTitleAttribute": strAXTitle,
		"kAXMinimizedAttribute": strAXMinimized, "kAXFrontmostAttribute": strAXFrontmost,
		"kAXRaiseAction": strAXRaise, "the prompt options dictionary": promptOptions,
	} {
		if s == 0 {
			t.Errorf("%s was not created", name)
		}
	}
	if responsibleForPID == nil {
		t.Log("responsibility_get_pid_responsible_for_pid did not resolve; " +
			"Status falls back to the parent process")
	}
}

// TrustedMustNotPrompt is the whole contract of [Trusted]: it is safe to call
// from a status line, a log line or a health check. There is no way to assert
// the absence of a dialog from inside the process, so what is asserted is the
// next best thing — that it is answerable, repeatable and cheap.
func TestTrustedIsSideEffectFree(t *testing.T) {
	a := Trusted()
	for i := 0; i < 50; i++ {
		if Trusted() != a {
			t.Fatal("Trusted() is not stable across calls")
		}
	}
	t.Logf("AXIsProcessTrusted() = %v", a)
}

func TestStatusDescribesThisProcess(t *testing.T) {
	st, err := Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Trusted != Trusted() {
		t.Errorf("Status().Trusted = %v, Trusted() = %v", st.Trusted, Trusted())
	}
	exe, _ := os.Executable()
	if st.Path != exe {
		t.Errorf("Status().Path = %q, want %q", st.Path, exe)
	}
	if st.Name == "" {
		t.Error("Status().Name is empty")
	}
	// A `go test` binary lives in a temporary directory, not in a .app.
	if st.Bundled {
		t.Errorf("a test binary reported itself as bundled: %+v", st)
	}
	if st.Advice() == "" {
		t.Error("Advice() is empty")
	}
	t.Logf("trust: %s", st)
	t.Logf("responsible process: %q", st.Responsible)
	t.Logf("advice: %s", st.Advice())
}

func TestDisplaysAgreeWithTheCoordinateSpace(t *testing.T) {
	ds, err := Displays()
	if err != nil {
		t.Fatalf("Displays: %v", err)
	}
	if len(ds) == 0 {
		t.Fatal("no displays")
	}
	main, ok := MainDisplay(ds)
	if !ok {
		t.Fatal("no main display")
	}
	// The whole package's coordinate space is defined by this: the main
	// display's origin IS the origin. If this ever fails, every rectangle in
	// the package means something else.
	if main.Bounds.X != 0 || main.Bounds.Y != 0 {
		t.Errorf("the main display is not at the origin: %v", main)
	}
	seen := map[uint32]bool{}
	mains := 0
	for _, d := range ds {
		if seen[d.ID] {
			t.Errorf("display %d listed twice", d.ID)
		}
		seen[d.ID] = true
		if d.Main {
			mains++
		}
		if d.Bounds.Empty() {
			t.Errorf("display %d has no extent: %v", d.ID, d)
		}
		t.Log(d)
	}
	if mains != 1 {
		t.Errorf("%d main displays, want exactly 1", mains)
	}
}

// Applications lists what has a user interface — and a Go binary, test binary
// included, is NOT one of them. macOS gives a plain executable an activation
// policy of Prohibited, which is exactly the class this package filters out, so
// this process does not appear in its own listing. That is the correct
// behaviour and it is asserted here so that a future change to the filter
// cannot quietly start returning several hundred background daemons.
func TestApplicationsListsOnlyThingsWithAUserInterface(t *testing.T) {
	apps, err := Applications()
	if err != nil {
		t.Fatalf("Applications: %v", err)
	}
	if len(apps) == 0 {
		t.Fatal("no applications at all")
	}
	seen := map[int]bool{}
	for _, a := range apps {
		if a.PID <= 0 {
			t.Errorf("application with a nonsense pid: %s", a)
		}
		if seen[a.PID] {
			t.Errorf("pid %d listed twice", a.PID)
		}
		seen[a.PID] = true
		if a.Name == "" {
			t.Errorf("application with no name: %s", a)
		}
	}
	if seen[os.Getpid()] {
		t.Error("this test binary appeared in the list; a Prohibited process has no windows " +
			"and asking AX about it costs a round-trip that can hang")
	}
	// The filter has to be doing something: -runningApplications on any Mac
	// returns far more processes than have a user interface.
	t.Logf("%d applications with a user interface", len(apps))
	if len(apps) > 250 {
		t.Errorf("%d applications is implausible; the Prohibited filter is not working", len(apps))
	}
}

// The window server's view needs no grant at all, which makes it the instrument
// the live suite measures a move with. Prove here that it works and that its
// rectangles are in the same space as CGDisplayBounds.
func TestServerWindowsAreInGlobalCoordinates(t *testing.T) {
	ws, err := ServerWindows()
	if err != nil {
		t.Fatalf("ServerWindows: %v", err)
	}
	if len(ws) == 0 {
		t.Skip("no on-screen windows on this machine")
	}
	ds, err := Displays()
	if err != nil {
		t.Fatalf("Displays: %v", err)
	}
	var onADisplay int
	for _, w := range ws {
		if w.PID <= 0 {
			t.Errorf("server window with a nonsense pid: %s", w)
		}
		for _, d := range ds {
			if !d.Bounds.Intersect(w.Frame).Empty() {
				onADisplay++
				break
			}
		}
	}
	// If the two spaces disagreed — the classic NSScreen bottom-left mixup —
	// almost nothing would land on a display.
	if onADisplay*2 < len(ws) {
		t.Errorf("only %d of %d server windows overlap any display; "+
			"the window list and the display list are not in the same coordinate space",
			onADisplay, len(ws))
	}
	t.Logf("%d on-screen windows, %d of them overlapping a display", len(ws), onADisplay)
}

// A closed handle must report ErrClosed from every method rather than passing a
// freed CFTypeRef to CoreFoundation, which would take the process down.
func TestClosedWindowHandleIsInert(t *testing.T) {
	if err := load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	// A handle on this very process. It needs no grant: a process is always
	// permitted to inspect itself... except that a Go test binary has no
	// windows, so what is exercised here is purely the closed-handle guard,
	// with no live element behind it.
	w := &AXWindow{pid: os.Getpid(), appName: "test"}
	if err := w.Close(); !errors.Is(err, ErrClosed) {
		t.Errorf("Close on a handle with no element = %v, want ErrClosed", err)
	}
	if _, err := w.Frame(); !errors.Is(err, ErrClosed) {
		t.Errorf("Frame = %v, want ErrClosed", err)
	}
	if err := w.SetPosition(Point{}); !errors.Is(err, ErrClosed) {
		t.Errorf("SetPosition = %v, want ErrClosed", err)
	}
	if err := w.SetSize(Size{}); !errors.Is(err, ErrClosed) {
		t.Errorf("SetSize = %v, want ErrClosed", err)
	}
	if err := w.Raise(); !errors.Is(err, ErrClosed) {
		t.Errorf("Raise = %v, want ErrClosed", err)
	}
	if _, err := w.Info(); !errors.Is(err, ErrClosed) {
		t.Errorf("Info = %v, want ErrClosed", err)
	}
	if w.PID() != os.Getpid() || w.App() != "test" || w.Title() != "" {
		t.Error("the accessors stopped working after Close")
	}
	CloseWindows([]*AXWindow{w}) // must not panic on an already-closed handle
}

// WindowsOf on a pid that owns nothing must come back with ErrNoWindow rather
// than a hard failure: in a listing of thirty applications two or three always
// do this, and the listing still has to be produced.
func TestWindowsOfAProcessWithNoWindows(t *testing.T) {
	if !Trusted() {
		t.Skip("not trusted for Accessibility; see TestLiveTrust for what that means")
	}
	// This test binary is an application with no windows.
	_, err := WindowsOf(os.Getpid(), "test")
	if err == nil {
		t.Fatal("a windowless process reported windows")
	}
	if !errors.Is(err, ErrNoWindow) && !strings.Contains(err.Error(), "AXError") {
		t.Errorf("WindowsOf = %v, want ErrNoWindow or an AXError", err)
	}
	t.Logf("a windowless process gives: %v", err)
}

// The pool helper must survive being called from a goroutine the scheduler is
// free to move between OS threads. Without the LockOSThread inside it, this
// crashes — measured, see the comment on pool.
func TestPoolSurvivesSchedulerPressure(t *testing.T) {
	if err := load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	done := make(chan struct{})
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
			}
		}()
	}
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			if _, err := Applications(); err != nil {
				t.Errorf("Applications: %v", err)
				return
			}
		}
	}()
	<-done
	close(stop)
}
