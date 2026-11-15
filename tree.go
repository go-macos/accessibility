package accessibility

import (
	"fmt"
	"io"
	"strings"
)

// Node is a detached snapshot of one accessibility element and everything under
// it. It carries no CoreFoundation reference, so it can be kept, compared,
// rendered and searched on any platform; it is what [Snapshot] returns and what
// a driver reasons over.
type Node struct {
	// Role is kAXRole ("AXButton", "AXTextField", "AXStaticText"...).
	Role string
	// Subrole is kAXSubrole; "AXSecureTextField" marks a password field.
	Subrole string
	// Title is kAXTitle.
	Title string
	// Value is kAXValue rendered as text. For a secure field it is ALWAYS
	// empty: [Snapshot] never copies a secure field's value out of the
	// process, so nothing downstream can leak it.
	Value string
	// Description is kAXDescription.
	Description string
	// Identifier is kAXIdentifier (Qt puts the object name here).
	Identifier string
	// Secure reports a secure text field. Its value was withheld.
	Secure bool
	// Children are the element's kAXChildren, in order.
	Children []Node
}

// Redacted is what [Node.Render] prints in place of a secure field's value.
const Redacted = "<redacted>"

// Walk calls fn on n and then on every descendant, depth first. A false return
// from fn stops the walk below that node (its siblings are still visited).
func (n *Node) Walk(fn func(n *Node, depth int) bool) { n.walk(fn, 0) }

func (n *Node) walk(fn func(*Node, int) bool, depth int) {
	if !fn(n, depth) {
		return
	}
	for i := range n.Children {
		n.Children[i].walk(fn, depth+1)
	}
}

// Find returns every node, in document order, for which match is true.
func (n *Node) Find(match func(*Node) bool) []*Node {
	var out []*Node
	n.Walk(func(c *Node, _ int) bool {
		if match(c) {
			out = append(out, c)
		}
		return true
	})
	return out
}

// FindRole returns every node whose role is role and, when title is not empty,
// whose title or value or description equals title.
func (n *Node) FindRole(role, title string) []*Node {
	return n.Find(func(c *Node) bool {
		if c.Role != role {
			return false
		}
		return title == "" || c.Title == title || c.Value == title || c.Description == title
	})
}

// Texts returns the title, value and description of every node that has one, in
// document order, skipping empties and secure fields. It is the "what does the
// window say" view of a tree.
func (n *Node) Texts() []string {
	var out []string
	n.Walk(func(c *Node, _ int) bool {
		for _, s := range []string{c.Title, c.Value, c.Description} {
			if s != "" && !c.Secure {
				out = append(out, s)
			}
		}
		return true
	})
	return out
}

// Render writes the tree, one element per line, indented by depth. A secure
// field's value is replaced by [Redacted] whatever it held.
func (n *Node) Render(w io.Writer) error {
	var err error
	n.Walk(func(c *Node, depth int) bool {
		if err != nil {
			return false
		}
		_, err = io.WriteString(w, c.line(depth)+"\n")
		return true
	})
	return err
}

func (n *Node) line(depth int) string {
	var b strings.Builder
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteString(n.Role)
	if n.Subrole != "" {
		fmt.Fprintf(&b, "/%s", n.Subrole)
	}
	for _, kv := range [][2]string{{"title", n.Title}, {"desc", n.Description}, {"id", n.Identifier}} {
		if kv[1] != "" {
			fmt.Fprintf(&b, " %s=%q", kv[0], kv[1])
		}
	}
	switch {
	case n.Secure:
		fmt.Fprintf(&b, " value=%q", Redacted)
	case n.Value != "":
		fmt.Fprintf(&b, " value=%q", n.Value)
	}
	return b.String()
}

// SecureSubrole is the subrole AX gives a password field.
const SecureSubrole = "AXSecureTextField"
