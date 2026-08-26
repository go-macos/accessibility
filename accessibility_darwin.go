//go:build darwin

package accessibility

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/go-macos/objc"
)

// The frameworks. The AX entry points and CGWindowListCopyWindowInfo are all
// re-exported by the ApplicationServices umbrella, which is the supported path
// to HIServices; CoreGraphics is opened separately for the display list.
const (
	frameworkApplicationServices = "/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices"
	frameworkCoreGraphics        = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
)

// The AXValueType tags AXValueCreate and AXValueGetValue take. Only the two
// this package needs are bound.
const (
	axValueTypeCGPoint int32 = 1
	axValueTypeCGSize  int32 = 2
)

// kAXTrustedCheckOptionPrompt is the option key AXIsProcessTrustedWithOptions
// looks for. ApplicationServices exports it as a CFStringRef variable, but the
// string it points at is the literal below and always has been; spelling it out
// keeps this package free of uintptr-to-pointer conversions, which is what go
// vet's unsafeptr check objects to. If a future macOS renamed it, the option
// would simply be ignored and the prompt would not appear — visible in
// [RequestTrust]'s return value, never a crash.
const trustedCheckOptionPrompt = "AXTrustedCheckOptionPrompt"

// CGWindowList options. On-screen windows only, and no desktop furniture.
const (
	cgWindowListOptionOnScreenOnly     uint32 = 1 << 0
	cgWindowListExcludeDesktopElements uint32 = 1 << 4
	cgNullWindowID                     uint32 = 0
)

// NSApplicationActivationPolicy. Prohibited processes have no user interface at
// all and are the great bulk of what -runningApplications returns.
const nsApplicationActivationPolicyProhibited int64 = 2

// maxDisplays bounds the CGGetActiveDisplayList buffer. macOS will not drive
// anything near this many.
const maxDisplays = 64

// cgPoint, cgSize and cgRect mirror the C structs passed and returned by value.
type (
	cgPoint struct{ X, Y float64 }
	cgSize  struct{ W, H float64 }
	cgRect  struct{ X, Y, W, H float64 }
)

// The C entry points, bound once by load.
var (
	axIsProcessTrusted            func() bool
	axIsProcessTrustedWithOptions func(options uintptr) bool
	axUIElementCreateApplication  func(pid int32) uintptr
	axUIElementCreateSystemWide   func() uintptr
	axUIElementGetPid             func(el uintptr, out *int32) int32
	axUIElementCopyAttributeValue func(el, attr uintptr, out *uintptr) int32
	axUIElementSetAttributeValue  func(el, attr, value uintptr) int32
	axUIElementPerformAction      func(el, action uintptr) int32
	axValueCreate                 func(typ int32, ptr unsafe.Pointer) uintptr
	axValueGetValue               func(v uintptr, typ int32, out unsafe.Pointer) bool

	cfRetain               func(uintptr) uintptr
	cfRelease              func(uintptr)
	cfGetTypeID            func(uintptr) uint64
	cfArrayGetTypeID       func() uint64
	cfArrayGetCount        func(uintptr) int64
	cfArrayGetValueAtIndex func(uintptr, int64) uintptr

	cfRunLoopRunInMode func(mode uintptr, seconds float64, returnAfterSourceHandled bool) int32

	cgWindowListCopyWindowInfo func(option, relativeTo uint32) uintptr
	cgRectFromDictionary       func(dict uintptr, out *cgRect) bool
	cgGetActiveDisplayList     func(max uint32, ids *uint32, count *uint32) int32
	cgDisplayBounds            func(id uint32) cgRect
	cgMainDisplayID            func() uint32

	// responsibleForPID is libsystem's
	// responsibility_get_pid_responsible_for_pid. It names the process that
	// actually holds this one's TCC grants, which for a command-line binary
	// is the terminal rather than the binary. It is SPI, so it is resolved
	// through Dlsym and left nil when it is not there; [Status] then falls
	// back to the parent process and says so.
	responsibleForPID func(pid int32) int32
)

// Retained CFStrings for the attributes and actions this package uses. They are
// created once and retained forever: an autoreleased NSString per call would
// need an autorelease pool per call, and see [pool] for why those are more
// delicate here than they look.
var (
	strAXWindows          uintptr
	strAXPosition         uintptr
	strAXSize             uintptr
	strAXTitle            uintptr
	strAXMinimized        uintptr
	strAXFrontmost        uintptr
	strAXRaise            uintptr
	strAXFocusedApp       uintptr
	strAXFocusedWindow    uintptr
	strCGWindowOwnerPID   uintptr
	strCGWindowOwnerName  uintptr
	strCGWindowName       uintptr
	strCGWindowNumber     uintptr
	strCGWindowLayer      uintptr
	strCGWindowBounds     uintptr
	numberYes             uintptr
	promptOptions         uintptr
	strRunLoopDefaultMode uintptr
)

var (
	loadOnce sync.Once
	loadErr  error
)

// pool runs fn inside an autorelease pool ON A PINNED OS THREAD.
//
// The pinning is not decoration. An NSAutoreleasePool belongs to the thread
// that created it, and Go will move an unlocked goroutine to another M at any
// preemption point — so a pool created on one thread and drained on another is
// a real possibility in ordinary code, and it segfaults inside libobjc. That
// was measured here, not guessed: the identical workload, 300 rounds, crashes
// every time unlocked and never once locked. Every path in this file that
// allocates an Objective-C object goes through this function.
func pool(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	objc.AutoreleasePool(fn)
}

// load resolves the frameworks and symbols once.
func load() error {
	loadOnce.Do(func() {
		if err := objc.Load(objc.Foundation, objc.AppKit,
			frameworkApplicationServices, frameworkCoreGraphics); err != nil {
			loadErr = fmt.Errorf("accessibility: loading the frameworks: %w", err)
			return
		}
		as, err := purego.Dlopen(frameworkApplicationServices, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = fmt.Errorf("accessibility: loading ApplicationServices: %w", err)
			return
		}
		cg, err := purego.Dlopen(frameworkCoreGraphics, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = fmt.Errorf("accessibility: loading CoreGraphics: %w", err)
			return
		}
		purego.RegisterLibFunc(&axIsProcessTrusted, as, "AXIsProcessTrusted")
		purego.RegisterLibFunc(&axIsProcessTrustedWithOptions, as, "AXIsProcessTrustedWithOptions")
		purego.RegisterLibFunc(&axUIElementCreateApplication, as, "AXUIElementCreateApplication")
		purego.RegisterLibFunc(&axUIElementCreateSystemWide, as, "AXUIElementCreateSystemWide")
		purego.RegisterLibFunc(&axUIElementGetPid, as, "AXUIElementGetPid")
		purego.RegisterLibFunc(&axUIElementCopyAttributeValue, as, "AXUIElementCopyAttributeValue")
		purego.RegisterLibFunc(&axUIElementSetAttributeValue, as, "AXUIElementSetAttributeValue")
		purego.RegisterLibFunc(&axUIElementPerformAction, as, "AXUIElementPerformAction")
		purego.RegisterLibFunc(&axValueCreate, as, "AXValueCreate")
		purego.RegisterLibFunc(&axValueGetValue, as, "AXValueGetValue")
		purego.RegisterLibFunc(&cfRetain, as, "CFRetain")
		purego.RegisterLibFunc(&cfRelease, as, "CFRelease")
		purego.RegisterLibFunc(&cfGetTypeID, as, "CFGetTypeID")
		purego.RegisterLibFunc(&cfArrayGetTypeID, as, "CFArrayGetTypeID")
		purego.RegisterLibFunc(&cfArrayGetCount, as, "CFArrayGetCount")
		purego.RegisterLibFunc(&cfArrayGetValueAtIndex, as, "CFArrayGetValueAtIndex")
		purego.RegisterLibFunc(&cfRunLoopRunInMode, as, "CFRunLoopRunInMode")
		purego.RegisterLibFunc(&cgWindowListCopyWindowInfo, as, "CGWindowListCopyWindowInfo")
		purego.RegisterLibFunc(&cgRectFromDictionary, as, "CGRectMakeWithDictionaryRepresentation")
		purego.RegisterLibFunc(&cgGetActiveDisplayList, cg, "CGGetActiveDisplayList")
		purego.RegisterLibFunc(&cgDisplayBounds, cg, "CGDisplayBounds")
		purego.RegisterLibFunc(&cgMainDisplayID, cg, "CGMainDisplayID")

		// SPI: present since macOS 10.14, but absence must not be fatal.
		if ls, err := purego.Dlopen(objc.LibSystem, purego.RTLD_NOW|purego.RTLD_GLOBAL); err == nil {
			if _, err := purego.Dlsym(ls, "responsibility_get_pid_responsible_for_pid"); err == nil {
				purego.RegisterLibFunc(&responsibleForPID, ls, "responsibility_get_pid_responsible_for_pid")
			}
		}

		pool(func() {
			keep := func(s string) uintptr { return cfRetain(uintptr(objc.NSString(s))) }
			strAXWindows = keep("AXWindows")
			strAXPosition = keep("AXPosition")
			strAXSize = keep("AXSize")
			strAXTitle = keep("AXTitle")
			strAXMinimized = keep("AXMinimized")
			strAXFrontmost = keep("AXFrontmost")
			strAXRaise = keep("AXRaise")
			strAXFocusedApp = keep("AXFocusedApplication")
			strAXFocusedWindow = keep("AXFocusedWindow")
			strCGWindowOwnerPID = keep("kCGWindowOwnerPID")
			strCGWindowOwnerName = keep("kCGWindowOwnerName")
			strCGWindowName = keep("kCGWindowName")
			strCGWindowNumber = keep("kCGWindowNumber")
			strCGWindowLayer = keep("kCGWindowLayer")
			strCGWindowBounds = keep("kCGWindowBounds")
			// kCFRunLoopDefaultMode is a CFStringRef variable whose value
			// is this literal. Spelling it out avoids dereferencing an
			// exported pointer, which go vet's unsafeptr check rejects.
			strRunLoopDefaultMode = keep("kCFRunLoopDefaultMode")
			numberYes = cfRetain(uintptr(objc.ClassID("NSNumber").Send(objc.Sel("numberWithBool:"), true)))
			promptOptions = cfRetain(uintptr(objc.ClassID("NSDictionary").Send(
				objc.Sel("dictionaryWithObject:forKey:"),
				objc.ID(numberYes), objc.NSString(trustedCheckOptionPrompt))))
		})
	})
	return loadErr
}

// ---------------------------------------------------------------------------
// Trust.
// ---------------------------------------------------------------------------

// Trusted reports whether this process may use the Accessibility API, WITHOUT
// any side effect: no dialog, no entry added to System Settings, nothing the
// user has to dismiss. It is AXIsProcessTrusted().
//
// Call this freely. If you want the dialog, call [RequestTrust] — and only
// because you decided to.
func Trusted() bool {
	if err := load(); err != nil {
		return false
	}
	return axIsProcessTrusted()
}

// RequestTrust asks macOS to show the "…would like to control this computer
// using accessibility features" dialog, and reports the trust state.
//
// THIS IS THE ONLY CALL IN THE PACKAGE THAT PROMPTS, and it always prompts when
// the process is not already trusted. It returns the state as it is at the
// moment of the call, which is essentially always false the first time: the
// dialog only offers to open System Settings, the user then has to switch the
// application on there, and macOS restarts the process for the change to take
// effect. Treat a false here as "asked, not granted", poll [Trusted], and tell
// the user what [Trust.Advice] says.
func RequestTrust() bool {
	if err := load(); err != nil {
		return false
	}
	return axIsProcessTrustedWithOptions(promptOptions)
}

// Status reports the trust state together with enough about this process to
// explain it. See [Trust] for why the explanation is needed: the grant does not
// belong to the executable that asks for it.
func Status() (Trust, error) {
	if err := load(); err != nil {
		return Trust{}, err
	}
	t := Trust{Trusted: axIsProcessTrusted()}
	if p, err := os.Executable(); err == nil {
		t.Path = p
		t.Name = filepath.Base(p)
		t.Bundled = strings.Contains(p, ".app/Contents/MacOS/")
	}
	pool(func() {
		if b := objc.ClassID("NSBundle").Send(objc.Sel("mainBundle")); b != 0 {
			t.Bundle = objc.GoString(b.Send(objc.Sel("bundleIdentifier")))
		}
		if t.Bundle != "" {
			t.Bundled = true
			if n := appNameForPID(int32(os.Getpid())); n != "" {
				t.Name = n
			}
		}
		t.Responsible = responsibleName()
	})
	return t, nil
}

// responsibleName names the process that holds this one's TCC grants. It asks
// libsystem first, because that is the authority macOS itself uses, and falls
// back to the parent process — which is the same thing in the ordinary case of
// a binary started from a shell, and is at least a true statement about the
// process tree when it is not.
func responsibleName() string {
	me := int32(os.Getpid())
	if responsibleForPID != nil {
		if rp := responsibleForPID(me); rp > 0 && rp != me {
			if n := appNameForPID(rp); n != "" {
				return n
			}
		}
	}
	if n := appNameForPID(int32(os.Getppid())); n != "" {
		return n
	}
	return ""
}

// appNameForPID returns the localised name of the application owning pid, or
// "" when that process is not a registered application (a shell, for instance).
// It must be called inside a [pool].
func appNameForPID(pid int32) string {
	a := objc.ClassID("NSRunningApplication").Send(
		objc.Sel("runningApplicationWithProcessIdentifier:"), pid)
	if a == 0 {
		return ""
	}
	return objc.GoString(a.Send(objc.Sel("localizedName")))
}

// ---------------------------------------------------------------------------
// Displays.
// ---------------------------------------------------------------------------

// Displays returns the active displays in global coordinates, straight from
// CoreGraphics.
//
// CGDisplayBounds, not NSScreen: the two disagree about where the origin is
// (NSScreen's is bottom-left), and NSScreen is a cache that is stale in a
// process with no running NSApp. This is the same space kAXPositionAttribute
// uses, so a rectangle from here can be handed to [MoveToDisplay] unchanged.
func Displays() ([]Display, error) {
	if err := load(); err != nil {
		return nil, err
	}
	ids := make([]uint32, maxDisplays)
	var n uint32
	if e := cgGetActiveDisplayList(maxDisplays, &ids[0], &n); e != 0 {
		return nil, fmt.Errorf("accessibility: CGGetActiveDisplayList: CGError %d", e)
	}
	main := cgMainDisplayID()
	out := make([]Display, 0, n)
	for _, id := range ids[:n] {
		b := cgDisplayBounds(id)
		out = append(out, Display{
			ID:     id,
			Bounds: Rect{b.X, b.Y, b.W, b.H},
			Main:   id == main,
		})
	}
	if len(out) == 0 {
		return nil, ErrNoDisplays
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Applications.
// ---------------------------------------------------------------------------

// Application is a running application that might own a movable window.
type Application struct {
	// PID is the process identifier — what AXUIElementCreateApplication
	// takes, and the only handle this package needs.
	PID int
	// Name is the localised application name, as the Dock shows it.
	Name string
	// Bundle is the bundle identifier, empty for the rare application that
	// has none.
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

// runLoopPumpSeconds is how long [Applications] runs the current thread's run
// loop before reading NSWorkspace's list. It is short enough not to be felt and
// long enough for a pending workspace notification to be delivered: measured,
// a launch became visible on the next 500 ms poll with this pump and NEVER
// became visible without it.
const runLoopPumpSeconds = 0.05

// Applications lists the running applications that have a user interface.
//
// Processes with an activation policy of Prohibited are left out: they are the
// great majority of what -[NSWorkspace runningApplications] returns, they have
// no windows by definition, and asking AX about each of them costs a round-trip
// that can hang. A plain Go binary — this package's own test binary included —
// is Prohibited, so it does not appear in its own listing. Regular and Accessory
// applications are both included: a menu-bar-only application can still own a
// window.
//
// # -runningApplications is a CACHE
//
// It is maintained by notifications delivered on a run loop, and a process that
// never runs one keeps whatever list it had when NSWorkspace was first touched
// — FOREVER. That was measured here, not assumed: with the run loop pumped, an
// application launched a moment earlier appeared within 500 ms; without it, the
// same launch was still invisible fifteen seconds later, and would have stayed
// invisible for the life of the process. A library cannot require its caller to
// run an AppKit run loop, so this function does two things about it.
//
// First it pumps the current thread's run loop for [runLoopPumpSeconds], which
// is what lets the notification through in an ordinary command-line process.
//
// Second — because that pump only reaches the run loop NSWorkspace's source is
// attached to, and a library cannot guarantee which thread it is called on —
// the list is completed from the WINDOW SERVER, which keeps no cache and is
// never stale. Any process the window server says owns an on-screen window is
// included even if NSWorkspace has never heard of it. Those entries carry the
// window server's name for the process and no bundle identifier, which is
// enough to reach them through [WindowsOf].
//
// This needs no Accessibility grant. [WindowsOf] does.
func Applications() ([]Application, error) {
	if err := load(); err != nil {
		return nil, err
	}
	var out []Application
	seen := map[int]bool{}
	pool(func() {
		cfRunLoopRunInMode(strRunLoopDefaultMode, runLoopPumpSeconds, false)
		ws := objc.ClassID("NSWorkspace").Send(objc.Sel("sharedWorkspace"))
		apps := ws.Send(objc.Sel("runningApplications"))
		n := int(apps.Send(objc.Sel("count")))
		for i := 0; i < n; i++ {
			a := apps.Send(objc.Sel("objectAtIndex:"), i)
			if objc.Send[int64](a, objc.Sel("activationPolicy")) == nsApplicationActivationPolicyProhibited {
				continue
			}
			pid := int(objc.Send[int32](a, objc.Sel("processIdentifier")))
			seen[pid] = true
			out = append(out, Application{
				PID:    pid,
				Name:   objc.GoString(a.Send(objc.Sel("localizedName"))),
				Bundle: objc.GoString(a.Send(objc.Sel("bundleIdentifier"))),
				Active: objc.Send[bool](a, objc.Sel("isActive")),
			})
		}
	})
	// The window server has the last word on what exists.
	sw, err := ServerWindows()
	if err != nil {
		return out, nil // the NSWorkspace list is still usable
	}
	for _, w := range sw {
		if w.Layer != 0 || w.PID <= 0 || seen[w.PID] {
			continue
		}
		seen[w.PID] = true
		out = append(out, Application{PID: w.PID, Name: w.Owner})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Windows.
// ---------------------------------------------------------------------------

// AXWindow is a live handle on another application's window: an AXUIElementRef
// for the window and one for its application, both retained. It implements
// [Window].
//
// Close it. The two CoreFoundation references it holds are not garbage
// collected, and an AXUIElementRef for a window that has since closed keeps the
// window server's record of it alive.
type AXWindow struct {
	mu      sync.Mutex
	app     uintptr // AXUIElementRef of the owning application
	el      uintptr // AXUIElementRef of the window
	pid     int
	appName string
	title   string
}

// PID returns the owning process.
func (w *AXWindow) PID() int { return w.pid }

// App returns the owning application's localised name.
func (w *AXWindow) App() string { return w.appName }

// Title returns the window title as it was when the handle was made. Titles
// change; call [AXWindow.Info] for a fresh one.
func (w *AXWindow) Title() string { return w.title }

// Close releases the two AXUIElementRefs. It is safe to call more than once,
// and every other method reports [ErrClosed] afterwards.
func (w *AXWindow) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.el == 0 {
		return ErrClosed
	}
	cfRelease(w.el)
	cfRelease(w.app)
	w.el, w.app = 0, 0
	return nil
}

// CloseWindows releases a whole listing.
func CloseWindows(ws []*AXWindow) {
	for _, w := range ws {
		_ = w.Close()
	}
}

// Frame reads the window's position and size back from the application, and is
// the instrument [Move] measures with. It really asks every time; nothing is
// remembered.
func (w *AXWindow) Frame() (Rect, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.el == 0 {
		return Rect{}, ErrClosed
	}
	var p cgPoint
	var s cgSize
	if err := readAXValue(w.el, strAXPosition, axValueTypeCGPoint,
		unsafe.Pointer(&p), "reading kAXPositionAttribute"); err != nil {
		return Rect{}, err
	}
	if err := readAXValue(w.el, strAXSize, axValueTypeCGSize,
		unsafe.Pointer(&s), "reading kAXSizeAttribute"); err != nil {
		return Rect{}, err
	}
	return Rect{p.X, p.Y, s.W, s.H}, nil
}

// readAXValue copies one AXValue attribute into out.
func readAXValue(el, attr uintptr, typ int32, out unsafe.Pointer, op string) error {
	var ref uintptr
	if err := AXError(axUIElementCopyAttributeValue(el, attr, &ref)).Err(op); err != nil {
		return err
	}
	if ref == 0 {
		return fmt.Errorf("accessibility: %s: the attribute came back empty", op)
	}
	defer cfRelease(ref)
	if !axValueGetValue(ref, typ, out) {
		return fmt.Errorf("accessibility: %s: AXValueGetValue refused the value (wrong type)", op)
	}
	return nil
}

// SetPosition writes kAXPositionAttribute.
//
// A success here means the application accepted the message, and NOTHING MORE.
// Use [Move], which reads the window back.
func (w *AXWindow) SetPosition(p Point) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.el == 0 {
		return ErrClosed
	}
	v := cgPoint{p.X, p.Y}
	return setAXValue(w.el, strAXPosition, axValueTypeCGPoint,
		unsafe.Pointer(&v), "setting kAXPositionAttribute")
}

// SetSize writes kAXSizeAttribute. The same caveat as [AXWindow.SetPosition]
// applies, and more often: a window with a minimum size accepts the message and
// keeps the size it had.
func (w *AXWindow) SetSize(s Size) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.el == 0 {
		return ErrClosed
	}
	v := cgSize{s.W, s.H}
	return setAXValue(w.el, strAXSize, axValueTypeCGSize,
		unsafe.Pointer(&v), "setting kAXSizeAttribute")
}

// setAXValue wraps a C struct in an AXValue and writes it to an attribute.
func setAXValue(el, attr uintptr, typ int32, ptr unsafe.Pointer, op string) error {
	v := axValueCreate(typ, ptr)
	if v == 0 {
		return fmt.Errorf("accessibility: %s: AXValueCreate returned nothing", op)
	}
	defer cfRelease(v)
	return AXError(axUIElementSetAttributeValue(el, attr, v)).Err(op)
}

// Raise brings the window to the front of its application's windows
// (kAXRaiseAction) and makes the application frontmost
// (kAXFrontmostAttribute).
//
// Both are needed. Raising alone reorders the window within an application that
// may itself be behind everything else, which from the user's point of view
// does nothing at all.
func (w *AXWindow) Raise() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.el == 0 {
		return ErrClosed
	}
	if err := AXError(axUIElementPerformAction(w.el, strAXRaise)).Err("performing kAXRaiseAction"); err != nil {
		return err
	}
	return AXError(axUIElementSetAttributeValue(w.app, strAXFrontmost, numberYes)).
		Err("setting kAXFrontmostAttribute")
}

// Info returns a fresh snapshot of the window. The Display field is left zero;
// fill it with [Annotate].
func (w *AXWindow) Info() (WindowInfo, error) {
	f, err := w.Frame()
	if err != nil {
		return WindowInfo{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	i := WindowInfo{PID: w.pid, App: w.appName, Frame: f}
	var titleRef uintptr
	if AXError(axUIElementCopyAttributeValue(w.el, strAXTitle, &titleRef)) == AXSuccess && titleRef != 0 {
		i.Title = objc.GoString(objc.ID(titleRef))
		cfRelease(titleRef)
		w.title = i.Title
	} else {
		i.Title = w.title
	}
	var minRef uintptr
	if AXError(axUIElementCopyAttributeValue(w.el, strAXMinimized, &minRef)) == AXSuccess && minRef != 0 {
		i.Minimized = objc.Send[bool](objc.ID(minRef), objc.Sel("boolValue"))
		cfRelease(minRef)
	}
	return i, nil
}

// WindowsOf returns handles on every window of one application.
//
// The caller owns the result and must [CloseWindows] it.
//
// An application that AX declines to describe — it is busy, it is hung, or it
// implements no accessibility at all, all of which report AXCannotComplete —
// yields [ErrNoWindow] rather than a hard failure, because in a listing of
// thirty applications two or three of them routinely do this and the listing
// still has to come back.
func WindowsOf(pid int, appName string) ([]*AXWindow, error) {
	if err := load(); err != nil {
		return nil, err
	}
	app := axUIElementCreateApplication(int32(pid))
	if app == 0 {
		return nil, fmt.Errorf("accessibility: AXUIElementCreateApplication(%d) returned nothing", pid)
	}
	var arr uintptr
	if err := AXError(axUIElementCopyAttributeValue(app, strAXWindows, &arr)).
		Err(fmt.Sprintf("reading kAXWindowsAttribute of pid %d", pid)); err != nil {
		cfRelease(app)
		return nil, err
	}
	if arr == 0 {
		cfRelease(app)
		return nil, ErrNoWindow
	}
	defer cfRelease(arr)
	// An application with no windows can answer with something that is not
	// an array at all — kCFNull, for one — and CFArrayGetCount on that
	// crashes the process rather than returning zero.
	if cfGetTypeID(arr) != cfArrayGetTypeID() {
		cfRelease(app)
		return nil, ErrNoWindow
	}
	n := cfArrayGetCount(arr)
	if n == 0 {
		cfRelease(app)
		return nil, ErrNoWindow
	}
	out := make([]*AXWindow, 0, n)
	pool(func() {
		for i := int64(0); i < n; i++ {
			el := cfArrayGetValueAtIndex(arr, i)
			if el == 0 {
				continue
			}
			w := &AXWindow{app: cfRetain(app), el: cfRetain(el), pid: pid, appName: appName}
			var titleRef uintptr
			if AXError(axUIElementCopyAttributeValue(el, strAXTitle, &titleRef)) == AXSuccess && titleRef != 0 {
				w.title = objc.GoString(objc.ID(titleRef))
				cfRelease(titleRef)
			}
			out = append(out, w)
		}
	})
	cfRelease(app)
	if len(out) == 0 {
		return nil, ErrNoWindow
	}
	return out, nil
}

// AllWindows returns handles on every window of every application with a user
// interface, this process's own included.
//
// The caller owns the result and must [CloseWindows] it. Applications that AX
// declines to describe are skipped; they are not an error, and on any real
// machine there are always a few.
func AllWindows() ([]*AXWindow, error) {
	if err := load(); err != nil {
		return nil, err
	}
	if !axIsProcessTrusted() {
		return nil, ErrNotTrusted
	}
	apps, err := Applications()
	if err != nil {
		return nil, err
	}
	var out []*AXWindow
	for _, a := range apps {
		ws, err := WindowsOf(a.PID, a.Name)
		if err != nil {
			continue
		}
		out = append(out, ws...)
	}
	return out, nil
}

// List returns a listing of every window on the machine, each attributed to the
// display it is mostly on, sorted for a person to read.
//
// It is the answer to "what is there, and where is it?" and holds no handles
// open: use [AllWindows] when you mean to move something.
func List() ([]WindowInfo, error) {
	displays, err := Displays()
	if err != nil {
		return nil, err
	}
	ws, err := AllWindows()
	if err != nil {
		return nil, err
	}
	defer CloseWindows(ws)
	out := make([]WindowInfo, 0, len(ws))
	for _, w := range ws {
		i, err := w.Info()
		if err != nil {
			continue
		}
		out = append(out, i)
	}
	out = Annotate(out, displays)
	SortWindows(out)
	return out, nil
}

// FocusedWindow returns a handle on the window that has keyboard focus right
// now — "this window", when the person wearing the glasses says "put THIS
// application on ribbon position 3".
//
// It goes through the system-wide AXUIElement, which is the only thing that
// knows what is focused: kAXFocusedApplicationAttribute, then that
// application's kAXFocusedWindowAttribute. NSWorkspace's -frontmostApplication
// would seem to do, and does not — it is fed by the same cache as
// -runningApplications, and in a process with no run loop it answers with
// whatever was true when the process started. See [Applications].
//
// The caller owns the result and must Close it.
func FocusedWindow() (*AXWindow, error) {
	if err := load(); err != nil {
		return nil, err
	}
	sys := axUIElementCreateSystemWide()
	if sys == 0 {
		return nil, fmt.Errorf("accessibility: AXUIElementCreateSystemWide returned nothing")
	}
	defer cfRelease(sys)

	var app uintptr
	if err := AXError(axUIElementCopyAttributeValue(sys, strAXFocusedApp, &app)).
		Err("reading kAXFocusedApplicationAttribute"); err != nil {
		return nil, err
	}
	if app == 0 {
		return nil, ErrNoWindow
	}
	defer cfRelease(app)

	var win uintptr
	if err := AXError(axUIElementCopyAttributeValue(app, strAXFocusedWindow, &win)).
		Err("reading kAXFocusedWindowAttribute"); err != nil {
		return nil, err
	}
	if win == 0 {
		return nil, ErrNoWindow
	}
	defer cfRelease(win)

	var pid int32
	if err := AXError(axUIElementGetPid(app, &pid)).Err("reading the focused application's pid"); err != nil {
		return nil, err
	}
	w := &AXWindow{app: cfRetain(app), el: cfRetain(win), pid: int(pid)}
	pool(func() {
		w.appName = appNameForPID(pid)
		var titleRef uintptr
		if AXError(axUIElementCopyAttributeValue(win, strAXTitle, &titleRef)) == AXSuccess && titleRef != 0 {
			w.title = objc.GoString(objc.ID(titleRef))
			cfRelease(titleRef)
		}
	})
	return w, nil
}

// ---------------------------------------------------------------------------
// The second instrument.
// ---------------------------------------------------------------------------

// ServerWindow is one window as the WINDOW SERVER sees it, from
// CGWindowListCopyWindowInfo.
//
// This is a SECOND, INDEPENDENT view of the same windows AX describes, and that
// is what it is for. AX asks the owning application where its window is; the
// window server knows where it actually put it. Reading a move back through the
// path that wrote it proves less than reading it back through a path that had
// nothing to do with the write, which is how this package's own tests establish
// that a window really moved.
//
// It needs no Accessibility grant. Title needs the Screen Recording grant and
// is empty without it; everything else here is unconditional.
type ServerWindow struct {
	// Number is the CGWindowID.
	Number int
	// PID is the owning process.
	PID int
	// Owner is the owning application's name.
	Owner string
	// Title is the window title, empty unless this process holds the Screen
	// Recording grant.
	Title string
	// Layer is the window level: 0 is an ordinary application window, and
	// anything else is furniture — menu bar items, the Dock, tooltips.
	Layer int
	// Frame is the window's rectangle in the same global coordinates AX
	// uses.
	Frame Rect
}

// String renders the window for a log line.
func (s ServerWindow) String() string {
	return fmt.Sprintf("#%d pid %d %s %q layer %d at %s", s.Number, s.PID, s.Owner, s.Title, s.Layer, s.Frame)
}

// ServerWindows lists the on-screen windows as the window server sees them.
func ServerWindows() ([]ServerWindow, error) {
	if err := load(); err != nil {
		return nil, err
	}
	arr := cgWindowListCopyWindowInfo(
		cgWindowListOptionOnScreenOnly|cgWindowListExcludeDesktopElements, cgNullWindowID)
	if arr == 0 {
		return nil, fmt.Errorf("accessibility: CGWindowListCopyWindowInfo returned nothing")
	}
	defer cfRelease(arr)
	var out []ServerWindow
	pool(func() {
		list := objc.ID(arr)
		n := int(list.Send(objc.Sel("count")))
		for i := 0; i < n; i++ {
			d := list.Send(objc.Sel("objectAtIndex:"), i)
			b := d.Send(objc.Sel("objectForKey:"), objc.ID(strCGWindowBounds))
			var r cgRect
			if b == 0 || !cgRectFromDictionary(uintptr(b), &r) {
				continue
			}
			out = append(out, ServerWindow{
				Number: int(numberFor(d, strCGWindowNumber)),
				PID:    int(numberFor(d, strCGWindowOwnerPID)),
				Owner:  objc.GoString(d.Send(objc.Sel("objectForKey:"), objc.ID(strCGWindowOwnerName))),
				Title:  objc.GoString(d.Send(objc.Sel("objectForKey:"), objc.ID(strCGWindowName))),
				Layer:  int(numberFor(d, strCGWindowLayer)),
				Frame:  Rect{r.X, r.Y, r.W, r.H},
			})
		}
	})
	return out, nil
}

// numberFor reads one NSNumber out of a window-info dictionary. A missing key
// gives nil, and -longLongValue on nil is zero, which is the right answer for
// every key read here.
func numberFor(dict objc.ID, key uintptr) int64 {
	return objc.Send[int64](dict.Send(objc.Sel("objectForKey:"), objc.ID(key)), objc.Sel("longLongValue"))
}
