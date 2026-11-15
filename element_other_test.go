//go:build !darwin

package accessibility

import (
	"errors"
	"testing"
	"time"
)

func TestElementStubsReportUnsupported(t *testing.T) {
	e, err := ApplicationElement(1)
	if e != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ApplicationElement = %v, %v", e, err)
	}
	e = &Element{}
	if !errors.Is(e.Close(), ErrClosed) {
		t.Error("Close")
	}
	if e.Role()+e.Subrole()+e.Title()+e.Description()+e.Identifier()+e.Value() != "" || e.Secure() {
		t.Error("attributes must be empty")
	}
	if k, err := e.Children(); k != nil || !errors.Is(err, ErrUnsupported) {
		t.Error("Children")
	}
	if n, err := e.Snapshot(SnapshotLimits{RedactEditable: true}); n != nil || !errors.Is(err, ErrUnsupported) {
		t.Error("Snapshot")
	}
	if l, err := e.Locate(func(*Node) bool { return true }, SnapshotLimits{}); l != nil || !errors.Is(err, ErrUnsupported) {
		t.Error("Locate")
	}
	if a, err := e.At(Path{0}); a != nil || !errors.Is(err, ErrUnsupported) {
		t.Error("At")
	}
	CloseElements([]*Element{e})
	if f, err := e.Frame(); f != (Rect{}) || !errors.Is(err, ErrUnsupported) {
		t.Error("Frame")
	}
	for name, err := range map[string]error{
		"SetValue": e.SetValue("x"), "Press": e.Press(), "Focus": e.Focus(),
		"TypeText": TypeText(1, "x", time.Millisecond), "PressKey": PressKey(1, KeyReturn),
		"ClickAt": ClickAt(1, Point{}, 1), "Click": e.Click(1, 1),
	} {
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s = %v", name, err)
		}
	}
	if KeyTab+KeySpace+KeyEscape == 0 {
		t.Error("key codes")
	}
}
