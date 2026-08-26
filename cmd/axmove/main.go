// Command axmove reports the Accessibility trust state, lists the windows on
// this machine, and moves one to a display.
//
// It is the tool to reach for when a user says "it will not move my window": it
// says in one line whether this process is trusted, whose grant that actually
// is, and what to do about it, before anything else is attempted.
//
//	axmove -trust                          # the trust state and what to do about it
//	axmove -displays                       # the displays, in global coordinates
//	axmove -list                           # every window, with its display
//	axmove -pid 1234 -display 5            # move that application's windows to display 5
//	axmove -focused -display 5             # move the window you are looking at
//	axmove -focused -rect 100,100,800,600  # move it to an explicit rectangle
//
// Nothing here ever shows the permission dialog unless -prompt is given.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/go-macos/accessibility"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is separated from main so every exit path is reachable from a test.
func run(args []string, out, errOut *os.File) int {
	fs := flag.NewFlagSet("axmove", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var (
		trust     = fs.Bool("trust", false, "report the Accessibility trust state and exit")
		prompt    = fs.Bool("prompt", false, "SHOW THE SYSTEM DIALOG asking for Accessibility, then exit")
		displays  = fs.Bool("displays", false, "list the displays and exit")
		list      = fs.Bool("list", false, "list every window and exit")
		pid       = fs.Int("pid", 0, "move the windows of this process")
		focused   = fs.Bool("focused", false, "move the window that has keyboard focus")
		display   = fs.Uint("display", 0, "CGDirectDisplayID to move to (see -displays)")
		rect      = fs.String("rect", "", "move to an explicit x,y,w,h in global coordinates instead")
		placement = fs.String("placement", "relative", "relative, origin, center or fill")
		inset     = fs.Float64("inset", 0, "shrink the target display by this many points on every side")
		noRaise   = fs.Bool("no-raise", false, "do not bring the window forward")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *prompt {
		fmt.Fprintf(out, "RequestTrust() = %v\n", accessibility.RequestTrust())
		return 0
	}

	st, err := accessibility.Status()
	if err != nil {
		fmt.Fprintf(errOut, "accessibility: %v\n", err)
		return 1
	}
	if *trust {
		fmt.Fprintf(out, "%s\n%s\n", st, st.Advice())
		return 0
	}

	if *displays {
		ds, err := accessibility.Displays()
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 1
		}
		for _, d := range ds {
			fmt.Fprintln(out, d)
		}
		return 0
	}

	if !st.Trusted {
		fmt.Fprintf(errOut, "%s\n%s\n", st, st.Advice())
		return 1
	}

	if *list {
		ws, err := accessibility.List()
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 1
		}
		for _, w := range ws {
			fmt.Fprintln(out, w)
		}
		return 0
	}

	p, err := parsePlacement(*placement)
	if err != nil {
		fmt.Fprintf(errOut, "%v\n", err)
		return 2
	}
	opts := &accessibility.Options{Placement: p, Inset: *inset, NoRaise: *noRaise}

	if *pid == 0 && !*focused {
		fs.Usage()
		return 2
	}
	var windows []*accessibility.AXWindow
	if *focused {
		w, err := accessibility.FocusedWindow()
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 1
		}
		windows = []*accessibility.AXWindow{w}
	} else {
		windows, err = accessibility.WindowsOf(*pid, "")
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 1
		}
	}
	defer accessibility.CloseWindows(windows)

	ds, err := accessibility.Displays()
	if err != nil {
		fmt.Fprintf(errOut, "%v\n", err)
		return 1
	}

	var target accessibility.Rect
	var toDisplay accessibility.Display
	if *rect != "" {
		if target, err = parseRect(*rect); err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return 2
		}
	} else {
		var ok bool
		if toDisplay, ok = accessibility.DisplayByID(ds, uint32(*display)); !ok {
			fmt.Fprintf(errOut, "no display with id %d; try -displays\n", *display)
			return 2
		}
	}

	status := 0
	for _, w := range windows {
		var res accessibility.Result
		if *rect != "" {
			res, err = accessibility.Move(w, target, opts)
		} else {
			res, err = accessibility.MoveToDisplay(w, toDisplay, ds, opts)
		}
		if err != nil {
			fmt.Fprintf(errOut, "pid %d %q: %v\n", w.PID(), w.Title(), err)
			status = 1
			continue
		}
		fmt.Fprintf(out, "pid %d %q: %s\n", w.PID(), w.Title(), res)
	}
	return status
}

// parsePlacement maps the flag to a [accessibility.Placement].
func parsePlacement(s string) (accessibility.Placement, error) {
	switch strings.ToLower(s) {
	case "relative":
		return accessibility.Relative, nil
	case "origin":
		return accessibility.Origin, nil
	case "center", "centre":
		return accessibility.Center, nil
	case "fill":
		return accessibility.Fill, nil
	}
	return 0, fmt.Errorf("unknown placement %q: want relative, origin, center or fill", s)
}

// parseRect reads "x,y,w,h" in global coordinates.
func parseRect(s string) (accessibility.Rect, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return accessibility.Rect{}, fmt.Errorf("bad -rect %q: want x,y,w,h", s)
	}
	var v [4]float64
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return accessibility.Rect{}, fmt.Errorf("bad -rect %q: %v", s, err)
		}
		v[i] = f
	}
	if v[2] <= 0 || v[3] <= 0 {
		return accessibility.Rect{}, fmt.Errorf("bad -rect %q: a window with no width or height", s)
	}
	return accessibility.Rect{X: v[0], Y: v[1], W: v[2], H: v[3]}, nil
}
