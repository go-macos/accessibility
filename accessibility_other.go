//go:build !darwin

package accessibility

import "fmt"

// This file is the non-darwin half of the package. Every exported symbol the
// darwin build provides is defined here too, so a consumer cross-compiles
// without build tags of its own and finds out at run time — with a clear
// error — that there is no Accessibility API on this platform.
//
// Note what is NOT stubbed out: the entire placement policy. [Place],
// [DisplayFor], [Move], [MoveToDisplay], [Annotate], [SortWindows], [AXError]
// and [Trust.Advice] are in the portable file and work here exactly as they do
// on macOS. That is deliberate. It is what lets the geometry, the display
// attribution, the clamping, the re-assert loop and — most of all — the
// read-back check that decides whether a window really moved be tested to the
// last branch on a Linux CI runner with no AX anywhere in sight.

// Trusted reports whether this process may use the Accessibility API. There is
// no such API here, so it is always false.
func Trusted() bool { return false }

// RequestTrust would show the macOS Accessibility dialog. There is none here,
// and nothing is prompted.
func RequestTrust() bool { return false }

// Status reports [ErrUnsupported].
func Status() (Trust, error) { return Trust{}, ErrUnsupported }

// Displays reports [ErrUnsupported]. Supply your own [Display] values to
// [MoveToDisplay] and [Place] to exercise the placement policy here.
func Displays() ([]Display, error) { return nil, ErrUnsupported }

// Application is a running application that might own a movable window. None
// is ever returned on this platform; the type exists so consumer code naming it
// still compiles.
type Application struct {
	// PID is the process identifier.
	PID int
	// Name is the localised application name.
	Name string
	// Bundle is the bundle identifier.
	Bundle string
	// Active reports whether this application is frontmost.
	Active bool
}

// String renders the application for a listing.
func (a Application) String() string {
	s := fmt.Sprintf("pid %d %s", a.PID, a.Name)
	if a.Bundle != "" {
		s += " (" + a.Bundle + ")"
	}
	if a.Active {
		s += " [active]"
	}
	return s
}

// Applications reports [ErrUnsupported].
func Applications() ([]Application, error) { return nil, ErrUnsupported }

// AXWindow is a live handle on another application's window. One can never be
// created here, so [WindowsOf] and [AllWindows] never hand one out; the type
// exists so consumer code naming it still compiles, and every method reports
// [ErrUnsupported] rather than panicking on a value a test constructed itself.
type AXWindow struct {
	pid     int
	appName string
	title   string
}

// PID returns the owning process.
func (w *AXWindow) PID() int { return w.pid }

// App returns the owning application's localised name.
func (w *AXWindow) App() string { return w.appName }

// Title returns the window title.
func (w *AXWindow) Title() string { return w.title }

// Close releases the handle. There is nothing to release here.
func (w *AXWindow) Close() error { return ErrUnsupported }

// CloseWindows releases a whole listing.
func CloseWindows(ws []*AXWindow) {
	for _, w := range ws {
		_ = w.Close()
	}
}

// Frame reports [ErrUnsupported].
func (w *AXWindow) Frame() (Rect, error) { return Rect{}, ErrUnsupported }

// SetPosition reports [ErrUnsupported].
func (w *AXWindow) SetPosition(Point) error { return ErrUnsupported }

// SetSize reports [ErrUnsupported].
func (w *AXWindow) SetSize(Size) error { return ErrUnsupported }

// Raise reports [ErrUnsupported].
func (w *AXWindow) Raise() error { return ErrUnsupported }

// Info reports [ErrUnsupported].
func (w *AXWindow) Info() (WindowInfo, error) { return WindowInfo{}, ErrUnsupported }

// WindowsOf reports [ErrUnsupported].
func WindowsOf(pid int, appName string) ([]*AXWindow, error) { return nil, ErrUnsupported }

// AllWindows reports [ErrUnsupported].
func AllWindows() ([]*AXWindow, error) { return nil, ErrUnsupported }

// List reports [ErrUnsupported].
func List() ([]WindowInfo, error) { return nil, ErrUnsupported }

// ServerWindow is one window as the macOS window server sees it. None is ever
// returned on this platform.
type ServerWindow struct {
	// Number is the CGWindowID.
	Number int
	// PID is the owning process.
	PID int
	// Owner is the owning application's name.
	Owner string
	// Title is the window title.
	Title string
	// Layer is the window level.
	Layer int
	// Frame is the window's rectangle.
	Frame Rect
}

// String renders the window for a log line.
func (s ServerWindow) String() string {
	return fmt.Sprintf("#%d pid %d %s %q layer %d at %s", s.Number, s.PID, s.Owner, s.Title, s.Layer, s.Frame)
}

// FocusedWindow reports [ErrUnsupported].
func FocusedWindow() (*AXWindow, error) { return nil, ErrUnsupported }

// ServerWindows reports [ErrUnsupported].
func ServerWindows() ([]ServerWindow, error) { return nil, ErrUnsupported }
