//go:build darwin && integration

package accessibility

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// The live suite. It really moves a real window belonging to a real other
// process.
//
//	ACCESSIBILITY_INTEGRATION=1 go test -tags integration -v -run TestLive .
//
// Every test here obeys three rules.
//
// FIRST: it moves ONLY a window this suite opened itself. TextEdit is launched
// with `open -n` so a fresh instance is used and the operator's own documents
// are never touched, and that instance is killed at the end. Nothing here ever
// moves a window that was already on screen.
//
// SECOND: a refusal is a skip, never a failure. This machine may not have
// granted Accessibility to the test binary — and on macOS the grant belongs to
// whatever launched it, so the same binary is trusted from one terminal and not
// from another. That is a fact about the machine, not about the package.
//
// THIRD: nothing is proved by a return code. AX accepts a write to
// kAXPositionAttribute with AXError 0 whether or not the application honours
// it, so every assertion here is made against a MEASUREMENT — and against a
// measurement taken through a path that did not perform the write.
//
// `open` and `osascript` are fixtures standing in for a person opening a
// window; the library itself starts no subprocesses and is pure Go.

func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv("ACCESSIBILITY_INTEGRATION") == "" {
		t.Skip("set ACCESSIBILITY_INTEGRATION=1 to run the live suite")
	}
}

func requireTrust(t *testing.T) {
	t.Helper()
	requireLive(t)
	st, err := Status()
	if err != nil {
		t.Skipf("Status: %v", err)
	}
	if !st.Trusted {
		// REFUSED, NOT FAILED. Say exactly what is missing and what it
		// would take, then get out of the way.
		t.Skipf("REFUSED: this process is not trusted for Accessibility.\n  state: %s\n  %s",
			st, st.Advice())
	}
}

// ---------------------------------------------------------------------------
// The trust state, reported honestly.
// ---------------------------------------------------------------------------

func TestLiveTrust(t *testing.T) {
	requireLive(t)
	st, err := Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	t.Logf("AXIsProcessTrusted()      = %v", st.Trusted)
	t.Logf("executable                = %s", st.Path)
	t.Logf("bundled                   = %v (bundle id %q)", st.Bundled, st.Bundle)
	t.Logf("responsible process       = %q", st.Responsible)
	t.Logf("state                     : %s", st)
	t.Logf("advice                    : %s", st.Advice())

	// The bundling question go-macos/hotkey ran into: is a refusal about the
	// grant, or about being an unbundled binary? Here it is answerable,
	// because AX says so directly. If AXIsProcessTrusted() is true for THIS
	// unbundled binary, then bundling is not what gates AX — the grant is,
	// and it is inherited from whatever launched it.
	switch {
	case st.Trusted && !st.Bundled:
		t.Log("FINDING: an UNBUNDLED Go binary IS trusted here. So AX is gated by the " +
			"TCC grant alone, not by bundling — unlike CGEventPost, which go-macos/hotkey " +
			"measured being refused to an unbundled binary that was already trusted.")
	case !st.Trusted && !st.Bundled:
		t.Log("FINDING: this unbundled binary is NOT trusted. That does not distinguish " +
			"'the grant is missing' from 'unbundled binaries cannot hold it' on its own; " +
			"grant Accessibility to the parent named above and re-run to tell them apart.")
	}
}

// TestLivePromptIsOptIn exercises the ONE call that shows a system dialog. It
// is behind a second switch on purpose: a test that pops a modal dialog on a
// machine nobody is watching is a bug.
func TestLivePromptIsOptIn(t *testing.T) {
	requireLive(t)
	if os.Getenv("ACCESSIBILITY_PROMPT") == "" {
		t.Skip("set ACCESSIBILITY_PROMPT=1 to let this test show the system dialog")
	}
	before := Trusted()
	got := RequestTrust()
	t.Logf("RequestTrust() = %v (Trusted() was %v)", got, before)
	if before && !got {
		t.Error("RequestTrust reported not-trusted for a process that is trusted")
	}
}

// ---------------------------------------------------------------------------
// A window of our own to move.
// ---------------------------------------------------------------------------

// openTextEdit launches a FRESH TextEdit instance and returns its pid and a
// handle on its window. It never touches an instance that was already running.
func openTextEdit(t *testing.T) (int, *AXWindow) {
	t.Helper()
	before := map[int]bool{}
	apps, err := Applications()
	if err != nil {
		t.Skipf("Applications: %v", err)
	}
	for _, a := range apps {
		before[a.PID] = true
	}
	if err := exec.Command("open", "-n", "-a", "TextEdit").Run(); err != nil {
		t.Skipf("could not launch TextEdit: %v", err)
	}

	// Find the NEW instance first and arrange for it to be killed no matter
	// what happens next, so a test that gives up does not leave a stray
	// TextEdit behind for the next one to trip over.
	var pid int
	deadline := time.Now().Add(15 * time.Second)
	for pid == 0 && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		apps, err := Applications()
		if err != nil {
			continue
		}
		for _, a := range apps {
			if a.Name == "TextEdit" && !before[a.PID] {
				pid = a.PID
				break
			}
		}
	}
	if pid == 0 {
		t.Skip("TextEdit did not start within 15s")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	// Then wait for its window. A freshly launched application answers
	// AXCannotComplete for a while — it is not hung, it has simply not
	// finished starting — so this retries rather than giving up on the first
	// refusal.
	var lastErr error
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		ws, err := WindowsOf(pid, "TextEdit")
		if err == nil {
			t.Cleanup(func() { CloseWindows(ws) })
			t.Logf("opened a fresh TextEdit, pid %d, %d window(s), %q",
				pid, len(ws), ws[0].Title())
			return pid, ws[0]
		}
		lastErr = err
		time.Sleep(400 * time.Millisecond)
	}
	t.Skipf("TextEdit (pid %d) never reported a window: %v", pid, lastErr)
	return 0, nil
}

// serverFrameOf reads a window's rectangle from the WINDOW SERVER — an
// instrument with no connection to the AX write path at all. Layer 0 is an
// ordinary application window; TextEdit's panels and shadows are not.
func serverFrameOf(t *testing.T, pid int) (Rect, bool) {
	t.Helper()
	ws, err := ServerWindows()
	if err != nil {
		t.Fatalf("ServerWindows: %v", err)
	}
	for _, w := range ws {
		if w.PID == pid && w.Layer == 0 && !w.Frame.Empty() {
			return w.Frame, true
		}
	}
	return Rect{}, false
}

// freshAXFrameOf re-reads the window through a BRAND NEW AXUIElement: a new
// AXUIElementCreateApplication, a new kAXWindowsAttribute copy, a new element.
// Nothing is shared with the handle that performed the write, so a stale value
// cached anywhere in this package could not produce this number.
func freshAXFrameOf(t *testing.T, pid int) (Rect, bool) {
	t.Helper()
	ws, err := WindowsOf(pid, "TextEdit")
	if err != nil {
		return Rect{}, false
	}
	defer CloseWindows(ws)
	f, err := ws[0].Frame()
	if err != nil {
		return Rect{}, false
	}
	return f, true
}

// ---------------------------------------------------------------------------
// The proof.
// ---------------------------------------------------------------------------

// proveMove is the whole experiment, with the write itself as a PARAMETER.
//
// Called with write=true it is the proof; called with write=false it is the
// negative control — the identical measurement with the one thing that matters
// taken out. The control has to fail. If it passes, the proof was measuring
// something else.
func proveMove(t *testing.T, w *AXWindow, pid int, want Rect, write bool) (ax, server Rect, ok bool) {
	t.Helper()

	beforeAX, err := w.Frame()
	if err != nil {
		t.Fatalf("reading the frame before: %v", err)
	}
	beforeServer, haveServer := serverFrameOf(t, pid)
	if !haveServer {
		t.Skip("the window server does not list this window (is the screen locked?)")
	}
	t.Logf("before: AX %s | window server %s", beforeAX, beforeServer)

	// The target must be FAR from where the window already is, or "it is
	// where I asked" proves nothing about the write.
	if d := math.Hypot(want.X-beforeAX.X, want.Y-beforeAX.Y); d < 200 {
		t.Fatalf("the target is only %g points from where the window already is; "+
			"that would not distinguish a move from a no-op", d)
	}

	if write {
		res, err := Move(w, want, &Options{NoRaise: true})
		if err != nil {
			t.Fatalf("Move: %v", err)
		}
		t.Logf("Move reported: %s", res)
	} else {
		t.Log("NEGATIVE CONTROL: the write is skipped; everything else is identical")
	}

	// Give the window server a moment to settle before asking it. AX answers
	// from the application, the server from its own records, and they are
	// not updated in the same breath.
	time.Sleep(300 * time.Millisecond)

	ax, okAX := freshAXFrameOf(t, pid)
	if !okAX {
		t.Fatal("could not re-read the window through a fresh AXUIElement")
	}
	server, okServer := serverFrameOf(t, pid)
	if !okServer {
		t.Fatal("the window vanished from the window server's list")
	}
	t.Logf("after : AX(fresh element) %s | window server %s", ax, server)

	const tol = 2.0
	okAXPos := near(ax.X, want.X, tol) && near(ax.Y, want.Y, tol)
	okSrvPos := near(server.X, want.X, tol) && near(server.Y, want.Y, tol)
	return ax, server, okAXPos && okSrvPos
}

// TestLiveMoveIsProvedByTwoIndependentInstruments is the test this package
// exists for.
func TestLiveMoveIsProvedByTwoIndependentInstruments(t *testing.T) {
	requireTrust(t)
	pid, w := openTextEdit(t)

	ds, err := Displays()
	if err != nil {
		t.Fatalf("Displays: %v", err)
	}
	main, _ := MainDisplay(ds)
	want := Rect{main.Bounds.X + 137, main.Bounds.Y + 211, 640, 480}

	ax, server, ok := proveMove(t, w, pid, want, true)
	if !ok {
		t.Fatalf("the window did not go where it was told: wanted %s, AX says %s, "+
			"the window server says %s", want, ax, server)
	}
	// The two instruments must also agree with EACH OTHER. If they did not,
	// one of them would be lying and the proof would rest on which one.
	if !near(ax.X, server.X, 2) || !near(ax.Y, server.Y, 2) {
		t.Errorf("the two instruments disagree: AX %s, window server %s", ax, server)
	}
	t.Logf("PROVED: the window is at %s, confirmed by a fresh AXUIElement AND by "+
		"CGWindowListCopyWindowInfo, which had nothing to do with the write", server)
}

// TestLiveNegativeControlTheProofFailsWithoutTheWrite runs the identical
// measurement with the write removed. It MUST report "not moved". Without this,
// the test above could be passing because TextEdit happens to open where it was
// asked to go.
func TestLiveNegativeControlTheProofFailsWithoutTheWrite(t *testing.T) {
	requireTrust(t)
	pid, w := openTextEdit(t)

	ds, err := Displays()
	if err != nil {
		t.Fatalf("Displays: %v", err)
	}
	main, _ := MainDisplay(ds)
	want := Rect{main.Bounds.X + 137, main.Bounds.Y + 211, 640, 480}

	ax, server, ok := proveMove(t, w, pid, want, false)
	if ok {
		t.Fatalf("THE CONTROL PASSED. Without any write at all, the window reads as being "+
			"at %s (server %s), which is where the proof asks it to go. The proof above is "+
			"therefore worthless — it is not measuring the write.", ax, server)
	}
	t.Logf("CONTROL HELD: with the write removed the window is still at %s, not %s, "+
		"so the assertion in the test above is really testing the write", server, want)
}

// TestLiveMoveIsRefusedVisiblyWhenNothingMoves proves the library's own
// no-op detector on a real window: asked to move to the position it ALREADY
// occupies, with the tolerance set to zero and a target a fraction of a point
// away, the window cannot comply and Move must say so rather than return
// success.
func TestLiveMoveReportsARefusalRatherThanPretending(t *testing.T) {
	requireTrust(t)
	pid, w := openTextEdit(t)
	_ = pid

	here, err := w.Frame()
	if err != nil {
		t.Fatalf("Frame: %v", err)
	}
	// Half a point away, with zero tolerance. The window server works in
	// whole points, so this is a target no window can reach.
	impossible := Rect{here.X + 0.5, here.Y, here.W, here.H}
	res, err := Move(w, impossible, &Options{Tolerance: -1, NoRaise: true})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("Move to an unreachable position returned %v (result %s); "+
			"a package that trusted AXError would have reported success", err, res)
	}
	t.Logf("REFUSAL REPORTED: %v", err)
}

// ---------------------------------------------------------------------------
// Displays.
// ---------------------------------------------------------------------------

func TestLiveMoveToAnotherDisplay(t *testing.T) {
	requireTrust(t)
	ds, err := Displays()
	if err != nil {
		t.Fatalf("Displays: %v", err)
	}
	if len(ds) < 2 {
		t.Skipf("only %d display attached; this test needs two", len(ds))
	}
	pid, w := openTextEdit(t)

	here, err := w.Frame()
	if err != nil {
		t.Fatalf("Frame: %v", err)
	}
	from, _ := DisplayFor(here, ds)
	var to Display
	for _, d := range ds {
		if d.ID != from.ID {
			to = d
			break
		}
	}
	t.Logf("moving from %s to %s", from, to)

	res, err := MoveToDisplay(w, to, ds, &Options{Placement: Relative})
	if err != nil {
		t.Fatalf("MoveToDisplay: %v", err)
	}
	t.Logf("MoveToDisplay reported: %s", res)
	time.Sleep(300 * time.Millisecond)

	// The instrument is the window server, not the AX handle that wrote.
	server, ok := serverFrameOf(t, pid)
	if !ok {
		t.Fatal("the window vanished from the window server's list")
	}
	// The proof is not "it is near a number" but "the window server puts its
	// centre on the display we named".
	if !to.Bounds.Contains(server.Center()) {
		t.Fatalf("the window server puts the window at %s, whose centre is not on %s",
			server, to)
	}
	got, _ := DisplayFor(server, ds)
	if got.ID != to.ID {
		t.Fatalf("the window is mostly on display %d, not the requested %d", got.ID, to.ID)
	}
	t.Logf("PROVED: %s is on %s, per the window server", server, to)

	// And back again, so the machine is left as it was found.
	if _, err := MoveToDisplay(w, from, ds, &Options{Placement: Relative}); err != nil {
		t.Logf("could not move it back: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Raising.
// ---------------------------------------------------------------------------

func TestLiveRaiseMakesTheApplicationFrontmost(t *testing.T) {
	requireTrust(t)
	pid, w := openTextEdit(t)

	// Put something else in front first, so "frontmost" afterwards means
	// something. Finder is always running.
	if err := exec.Command("osascript", "-e", `tell application "Finder" to activate`).Run(); err != nil {
		t.Skipf("could not put another application in front: %v", err)
	}
	if !waitForFocus(t, func(p int) bool { return p != pid }, 5*time.Second) {
		t.Skipf("could not get TextEdit (pid %d) out of the front to begin with; "+
			"the focused application is still %d", pid, focusedPID(t))
	}
	t.Logf("another application is in front (pid %d)", focusedPID(t))

	if err := w.Raise(); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if !waitForFocus(t, func(p int) bool { return p == pid }, 5*time.Second) {
		t.Fatalf("after Raise the focused application is pid %d, not TextEdit's %d",
			focusedPID(t), pid)
	}
	t.Logf("PROVED: after Raise, the system-wide AX element reports pid %d (TextEdit) focused", pid)
}

// focusedPID asks the SYSTEM-WIDE AXUIElement which application has focus.
// NSWorkspace's -frontmostApplication is fed by the cache described on
// [Applications] and answers with history in a process that runs no run loop;
// this does not.
func focusedPID(t *testing.T) int {
	t.Helper()
	w, err := FocusedWindow()
	if err != nil {
		return 0
	}
	defer w.Close()
	return w.PID()
}

func waitForFocus(t *testing.T, ok func(int) bool, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok(focusedPID(t)) {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

// TestLiveFocusedWindow proves the "put THIS application on ribbon position 3"
// entry point: whatever Raise just brought forward is what FocusedWindow finds.
func TestLiveFocusedWindow(t *testing.T) {
	requireTrust(t)
	pid, w := openTextEdit(t)
	if err := w.Raise(); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if !waitForFocus(t, func(p int) bool { return p == pid }, 5*time.Second) {
		t.Skipf("TextEdit did not take focus; focused pid is %d", focusedPID(t))
	}
	f, err := FocusedWindow()
	if err != nil {
		t.Fatalf("FocusedWindow: %v", err)
	}
	defer f.Close()
	if f.PID() != pid {
		t.Fatalf("FocusedWindow reports pid %d, want %d", f.PID(), pid)
	}
	// It must be a usable handle, not just an identity: read the frame, and
	// check it against the window server.
	fr, err := f.Frame()
	if err != nil {
		t.Fatalf("Frame through the focused handle: %v", err)
	}
	sv, ok := serverFrameOf(t, pid)
	if !ok {
		t.Skip("the window server does not list this window")
	}
	if !near(fr.X, sv.X, 2) || !near(fr.Y, sv.Y, 2) {
		t.Errorf("the focused handle says %s, the window server says %s", fr, sv)
	}
	t.Logf("PROVED: FocusedWindow is pid %d %q at %s, confirmed by the window server",
		f.PID(), f.Title(), fr)
}

// ---------------------------------------------------------------------------
// The listing, and the two views agreeing across the whole machine.
// ---------------------------------------------------------------------------

func TestLiveListing(t *testing.T) {
	requireTrust(t)
	ws, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ws) == 0 {
		t.Skip("no windows on this machine")
	}
	for _, w := range ws {
		if w.PID <= 0 {
			t.Errorf("window with a nonsense pid: %s", w)
		}
		t.Log(w)
	}
	var attributed int
	for _, w := range ws {
		if w.Display != 0 {
			attributed++
		}
	}
	if attributed != len(ws) {
		t.Errorf("%d of %d windows could not be attributed to a display",
			len(ws)-attributed, len(ws))
	}
}

// The two views of the same window must agree. They are produced by completely
// different machinery — one asks the application, the other asks the window
// server — so a systematic disagreement would mean this package has the
// coordinate space wrong somewhere, which is exactly the bug that sends a
// window to a plausible-looking wrong place.
func TestLiveAXAndTheWindowServerAgree(t *testing.T) {
	requireTrust(t)
	axWindows, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	server, err := ServerWindows()
	if err != nil {
		t.Fatalf("ServerWindows: %v", err)
	}
	byPID := map[int][]Rect{}
	for _, s := range server {
		if s.Layer == 0 {
			byPID[s.PID] = append(byPID[s.PID], s.Frame)
		}
	}
	var compared, agreed int
	var worst string
	for _, a := range axWindows {
		if a.Minimized {
			continue // a minimized window is not on screen to compare with
		}
		for _, s := range byPID[a.PID] {
			if !near(s.W, a.Frame.W, 4) || !near(s.H, a.Frame.H, 4) {
				continue // a different window of the same application
			}
			compared++
			if near(s.X, a.Frame.X, 4) && near(s.Y, a.Frame.Y, 4) {
				agreed++
			} else if worst == "" {
				worst = fmt.Sprintf("%s: AX %s, window server %s", a.App, a.Frame, s)
			}
			break
		}
	}
	if compared == 0 {
		t.Skip("no window could be matched between the two views")
	}
	t.Logf("%d of %d matched windows agree between AX and the window server", agreed, compared)
	// Some disagreement is normal — a window being dragged, a sheet — but a
	// systematic one is a coordinate-space bug.
	if agreed*4 < compared*3 {
		t.Errorf("only %d of %d agree; the two coordinate spaces have drifted apart. %s",
			agreed, compared, worst)
	}
}

// TestLiveRoleAndAttributesSayWhatAnElementIs covers the diagnostic that was
// missing the day an application answered kAXWindows with elements that had no
// position: with only "the element does not have that attribute" to go on there
// was no way to ask what the elements WERE, and an hour went into guessing.
func TestLiveRoleAndAttributesSayWhatAnElementIs(t *testing.T) {
	requireTrust(t)
	pid, w := openTextEdit(t)
	_ = pid

	role, subrole, err := w.Role()
	if err != nil {
		t.Fatalf("Role: %v", err)
	}
	if role != "AXWindow" {
		t.Errorf("role = %q, want AXWindow for a window TextEdit just opened", role)
	}
	t.Logf("role=%q subrole=%q", role, subrole)

	attrs, err := w.Attributes()
	if err != nil {
		t.Fatalf("Attributes: %v", err)
	}
	if len(attrs) == 0 {
		t.Fatal("Attributes returned nothing for a live window")
	}
	// Sorted, and carrying the two attributes every mover needs.
	for i := 1; i < len(attrs); i++ {
		if attrs[i-1] > attrs[i] {
			t.Errorf("Attributes are not sorted: %q before %q", attrs[i-1], attrs[i])
			break
		}
	}
	need := map[string]bool{"AXPosition": false, "AXSize": false, "AXRole": false}
	for _, a := range attrs {
		if _, ok := need[a]; ok {
			need[a] = true
		}
	}
	for a, found := range need {
		if !found {
			t.Errorf("a live window does not list %s; it lists %v", a, attrs)
		}
	}
	t.Logf("%d attributes: %v", len(attrs), attrs)

	// A closed handle must answer, not crash: the diagnostic is most likely to
	// be reached from an error path, where the handle's state is unknown.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, err := w.Role(); !errors.Is(err, ErrClosed) {
		t.Errorf("Role after Close = %v, want ErrClosed", err)
	}
	if _, err := w.Attributes(); !errors.Is(err, ErrClosed) {
		t.Errorf("Attributes after Close = %v, want ErrClosed", err)
	}
}

// TestLiveEveryWindowReallyIsAWindow, on this machine, for every application
// that will answer.
//
// kAXWindows is not a list of windows. The Finder answers it with its DESKTOP —
// an AXScrollArea covering every display: 19336x2529 at -17280,-1200 here — and
// a caller that moves what this returns then tries to move the desktop. It
// fails after two attempts and reports "the window did not move where it was
// told", which is an accusation aimed at something that was never a window.
//
// Seen in go-xrkit/desk's own log, placing applications on a ribbon.
func TestLiveEveryWindowReallyIsAWindow(t *testing.T) {
	requireLive(t)

	apps, err := Applications()
	if err != nil {
		t.Fatalf("Applications: %v", err)
	}
	var checked int
	for _, a := range apps {
		ws, err := WindowsOf(a.PID, a.Name)
		if err != nil {
			continue // an application AX will not describe is not this test's business
		}
		for _, w := range ws {
			role, sub, err := w.Role()
			if err != nil {
				w.Close()
				continue
			}
			checked++
			if role != RoleWindow {
				t.Errorf("%s handed back a %s (%s) as a window: %s", a.Name, role, sub, frameOf(w))
			}
			w.Close()
		}
	}
	if checked == 0 {
		t.Skip("nothing on this machine answered with a window")
	}
	t.Logf("%d windows across %d applications, every one an %s", checked, len(apps), RoleWindow)
}

// frameOf is for the message.
func frameOf(w *AXWindow) string {
	f, err := w.Frame()
	if err != nil {
		return "unreadable"
	}
	return f.String()
}
