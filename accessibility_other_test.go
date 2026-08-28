//go:build !darwin

package accessibility

import (
	"errors"
	"strings"
	"testing"
)

// Off darwin there is no excuse for anything in this package to be uncovered:
// all that exists here is the portable policy — which accessibility_test.go
// exercises to the last branch — plus these stubs. The CI gate is on the whole
// package, not on a subset.

func TestStubsAllReportUnsupported(t *testing.T) {
	if Trusted() {
		t.Error("Trusted() is true on a platform with no Accessibility API")
	}
	if RequestTrust() {
		t.Error("RequestTrust() is true on a platform with no Accessibility API")
	}
	if _, err := Status(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Status = %v", err)
	}
	if _, err := Displays(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Displays = %v", err)
	}
	if _, err := Applications(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Applications = %v", err)
	}
	if _, err := WindowsOf(1, "Finder"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("WindowsOf = %v", err)
	}
	if _, err := AllWindows(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("AllWindows = %v", err)
	}
	if _, err := List(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("List = %v", err)
	}
	if _, err := FocusedWindow(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("FocusedWindow = %v", err)
	}
	if _, err := ServerWindows(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("ServerWindows = %v", err)
	}
}

// A consumer that keeps an *AXWindow in a struct must be able to compile and
// run its code here — including calling into it — and get a clear error rather
// than a panic.
func TestStubWindow(t *testing.T) {
	w := &AXWindow{pid: 42, appName: "TextEdit", title: "Untitled"}
	if w.PID() != 42 || w.App() != "TextEdit" || w.Title() != "Untitled" {
		t.Errorf("accessors = %d %q %q", w.PID(), w.App(), w.Title())
	}
	if _, err := w.Frame(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Frame = %v", err)
	}
	if err := w.SetPosition(Point{1, 2}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("SetPosition = %v", err)
	}
	if err := w.SetSize(Size{3, 4}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("SetSize = %v", err)
	}
	if err := w.Raise(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Raise = %v", err)
	}
	if _, err := w.Info(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Info = %v", err)
	}
	if _, _, err := w.Role(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Role = %v", err)
	}
	if _, err := w.Attributes(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Attributes = %v", err)
	}
	if err := w.Close(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Close = %v", err)
	}
	CloseWindows([]*AXWindow{w}) // must not panic
}

// The policy still refuses to move a window here, for the right reason: the
// stub cannot read a frame, so Move reports that rather than pretending.
func TestMoveThroughTheStubWindow(t *testing.T) {
	if _, err := Move(&AXWindow{}, Rect{0, 0, 10, 10}, nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Move through the stub = %v", err)
	}
}

func TestStubStringers(t *testing.T) {
	a := Application{PID: 7, Name: "Finder", Bundle: "com.apple.finder", Active: true}
	if got := a.String(); got != "pid 7 Finder (com.apple.finder) [active]" {
		t.Errorf("Application.String = %q", got)
	}
	if got := (Application{PID: 8, Name: "helper"}).String(); got != "pid 8 helper" {
		t.Errorf("Application.String (bare) = %q", got)
	}
	s := ServerWindow{Number: 3, PID: 9, Owner: "Finder", Title: "Downloads",
		Layer: 0, Frame: Rect{0, 0, 100, 50}}
	if got := s.String(); !strings.Contains(got, `#3 pid 9 Finder "Downloads" layer 0 at 0,0 100x50`) {
		t.Errorf("ServerWindow.String = %q", got)
	}
}
