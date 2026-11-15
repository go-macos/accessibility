package accessibility

import (
	"errors"
	"strings"
	"testing"
)

func sample() *Node {
	return &Node{Role: "AXApplication", Children: []Node{
		{Role: "AXWindow", Title: "Open", Children: []Node{
			{Role: "AXStaticText", Value: "Enter password"},
			{Role: "AXTextField", Subrole: SecureSubrole, Secure: true, Value: "hunter2", Identifier: "pw"},
			{Role: "AXButton", Title: "OK", Description: "confirm"},
		}},
	}}
}

func TestRenderRedactsSecureValue(t *testing.T) {
	var b strings.Builder
	if err := sample().Render(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, "hunter2") {
		t.Fatalf("secure value leaked:\n%s", out)
	}
	for _, want := range []string{`value="` + Redacted + `"`, `AXButton title="OK" desc="confirm"`, `AXTextField/AXSecureTextField id="pw"`, `value="Enter password"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

type failWriter struct{ n int }

func (f *failWriter) Write(p []byte) (int, error) {
	f.n++
	return 0, errors.New("boom")
}

func TestRenderStopsOnWriteError(t *testing.T) {
	w := &failWriter{}
	if err := sample().Render(w); err == nil {
		t.Fatal("want the write error")
	}
	if w.n != 1 {
		t.Fatalf("kept writing after an error: %d writes", w.n)
	}
}

func TestFindAndTexts(t *testing.T) {
	n := sample()
	if got := n.FindRole("AXButton", "OK"); len(got) != 1 {
		t.Fatalf("FindRole = %d", len(got))
	}
	if got := n.FindRole("AXButton", "Cancel"); len(got) != 0 {
		t.Fatalf("FindRole(Cancel) = %d", len(got))
	}
	if got := n.FindRole("AXWindow", ""); len(got) != 1 {
		t.Fatalf("FindRole any title = %d", len(got))
	}
	texts := strings.Join(n.Texts(), "|")
	if texts != "Open|Enter password|OK|confirm" {
		t.Fatalf("Texts = %q", texts)
	}
}

func TestWalkPrunes(t *testing.T) {
	seen := 0
	sample().Walk(func(n *Node, d int) bool { seen++; return d < 1 })
	if seen != 2 {
		t.Fatalf("walk visited %d, want 2 (root and window, pruned below)", seen)
	}
}
