# go-macos/accessibility

[![CI](https://github.com/go-macos/accessibility/actions/workflows/ci.yml/badge.svg)](https://github.com/go-macos/accessibility/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-macos/accessibility.svg)](https://pkg.go.dev/github.com/go-macos/accessibility)
[![coverage](https://img.shields.io/badge/coverage-100%25%20portable%20layer-brightgreen)](https://github.com/go-macos/accessibility/actions/workflows/ci.yml)
[![license](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)

**Move another application's window to a chosen display, from pure Go with
`CGO_ENABLED=0`** — the macOS Accessibility (AX) API through
[purego](https://github.com/ebitengine/purego), with no cgo anywhere.

```go
w, err := accessibility.FocusedWindow()   // "put THIS application…"
if err != nil {
        return err
}
defer w.Close()

displays, _ := accessibility.Displays()
panel, _ := accessibility.DisplayByID(displays, ribbonPosition3)

res, err := accessibility.MoveToDisplay(w, panel, displays, nil)
if err != nil {
        return err // it did not go there, and this says how far off it landed
}
log.Print(res) // 4915,155 603x505 → wanted 137,211 640x480, got 137,211 640x480
```

The consumer is [`go-xrkit/desk`](https://github.com/go-xrkit), which puts
several virtual displays on a 360° ribbon inside AR glasses. The person wearing
them says "put this application on ribbon position 3" instead of dragging a
window across a display boundary they cannot see the edge of. AX is the only
supported way for one process to move another's window on macOS, so AX is what
this binds.

## A status code proves nothing here

This is the single most important thing about the AX API, and the reason a naive
implementation is worse than useless.

**A write to `kAXPositionAttribute` returns `kAXErrorSuccess` whether or not the
application honours it.** A window pinned by its own controller, a full-screen
window, a sheet, a window whose application is busy — all of them accept the
message, return 0, and stay exactly where they were. A caller that checked the
return value would be told the move worked.

So the return value is not what this package checks. [`Move`] writes the
position, writes the size, and then **reads the window back** and compares. If
the window is not where it was told, it reports `ErrRefused` with the measured
difference, and does not raise the window either — bringing a window forward that
is still on the wrong display is worse than leaving it alone.

```
accessibility: the window did not move where it was told: 4915,155 603x505 →
wanted 4915.5,155 603x505, got 4915,155 603x505 (NOT MOVED: off by -0.5,0) after 2 attempts
```

**Two attempts is normal, not padding.** Setting the size can move the origin —
the window server refuses to leave a newly enlarged window hanging off an edge —
so the position is written, then the size, then the position again if the
read-back disagrees.

**A refused *size* is not a failed move.** A terminal that snaps to whole
character cells, or any window with a minimum size, keeps the size it had.
`Result.Moved` and `Result.Resized` are separate, and only the first is an
error.

## The coordinate space, and the trap in it

Every rectangle here is in **global display coordinates: origin at the top-left
of the main display, y increasing downwards.** That is the space of
`kAXPositionAttribute`, of `CGDisplayBounds` and of
`CGWindowListCopyWindowInfo` — three independent instruments that agree.

It is **not** `NSScreen`'s space, whose origin is bottom-left. Mixing the two is
the classic way to send a window to a plausible-looking wrong place, and it is
why `NSScreen` is not consulted anywhere in this package. (It is also a cache
that is stale in a process with no running `NSApp`; see
[go-macos/virtualdisplay](https://github.com/go-macos/virtualdisplay).)

`TestLiveAXAndTheWindowServerAgree` asserts the two views agree across the whole
machine, so a drift between the spaces fails a test rather than misplacing a
window.

## Permission — and who actually holds it

Unlike [go-macos/hotkey](https://github.com/go-macos/hotkey), which found a
permission-free route to system-wide shortcuts, **this package needs the
Accessibility (TCC) grant.** There is no alternative: AX is the API, and AX is
gated.

Two calls, and only one of them prompts.

| call | dialog |
|---|---|
| `Trusted()` — `AXIsProcessTrusted()` | **none.** Safe from a status line. |
| `RequestTrust()` — `AXIsProcessTrustedWithOptions` with `kAXTrustedCheckOptionPrompt` | **yes, always**, when not already trusted |

**The grant does not belong to the executable that asks for it.** It belongs to
the *responsible process*, which for a command-line binary is the terminal that
launched it. An unbundled Go binary is trusted exactly when its terminal is, will
never appear in System Settings under its own name, and **cannot be granted the
permission on its own**. Telling a user to "add this binary in System Settings"
when that is impossible is worse than telling them nothing, so `Status()` works
out which case it is and `Trust.Advice()` says the true thing:

```
trusted (accessibility.test, unbundled binary)
accessibility.test is trusted for Accessibility, by way of whatever launched it
(Code); nothing to do, but note that the grant belongs to that parent and not to
this binary, so running it from somewhere else may not be trusted.
```

The responsible process is found with libsystem's
`responsibility_get_pid_responsible_for_pid`, which is SPI, so it is resolved
through `dlsym` and its absence falls back to the parent process rather than
failing.

**On the bundling question `go-macos/hotkey` ran into:** that package measured
`CGEventPost` being *silently refused to an unbundled binary that was already
trusted*, so it could not tell the grant from the bundling. Here it is
answerable, and the answer is different. An unbundled `go test` binary with
`AXIsProcessTrusted() == true` moves another application's window without
complaint — proved below. **AX is gated by the TCC grant alone, not by
bundling.**

## `-runningApplications` is a CACHE — measured

`-[NSWorkspace runningApplications]` is maintained by notifications delivered on
a run loop. **A process that never runs one keeps whatever list it had when
NSWorkspace was first touched, for the life of the process.** Not "briefly
stale": permanently wrong.

That was measured, not assumed. Identical program, one line different:

```
nopump  before: 133 apps, TextEdit: []
          NEVER SAW IT after 15s
pump    before: 133 apps, TextEdit: []
          t=0.5s SAW IT: apps=134 TextEdit=[20745]
```

`Applications()` therefore does two things about it. It pumps the current
thread's run loop for 50 ms, which is what lets the notification through in an
ordinary command-line process; and — because that only reaches the run loop
NSWorkspace's source is attached to, and a library cannot dictate which thread
it is called on — **it completes the list from the window server**, which keeps
no cache. Any process `CGWindowListCopyWindowInfo` says owns an on-screen window
is included even if NSWorkspace has never heard of it.

`FocusedWindow()` avoids the cache entirely by going through the system-wide
`AXUIElement` (`kAXFocusedApplicationAttribute` →
`kAXFocusedWindowAttribute`) rather than `-frontmostApplication`.

## An autorelease pool needs its thread pinned — measured

`NSAutoreleasePool` belongs to the thread that created it, and Go moves an
unlocked goroutine to another M at any preemption point. A pool created on one
thread and drained on another **segfaults inside libobjc**, and it is not a rare
race:

```
unlocked  SIGSEGV: segmentation violation  PC=0x18c727c60  addr=0x10
locked    survived 300 rounds
unlocked  SIGSEGV: segmentation violation  PC=0x18c727c60  addr=0x10
locked    survived 300 rounds
```

Every path in this package that allocates an Objective-C object goes through one
helper that holds `runtime.LockOSThread` for the life of the pool. This is worth
knowing for anyone using `go-macos/objc`'s `AutoreleasePool` directly, which does
not pin the thread itself.

## The API

**Trust.** `Trusted()`, `RequestTrust()` (the only call that prompts),
`Status() (Trust, error)`, `Trust.Advice()`.

**Displays.** `Displays() ([]Display, error)` from `CGGetActiveDisplayList` and
`CGDisplayBounds`. `DisplayByID`, `MainDisplay`, `DisplayFor`.

**Listing.** `Applications()`, `WindowsOf(pid, name)`, `AllWindows()`,
`FocusedWindow()`, `List()` — pid, application name, window title, position,
size and the display each window is on. `ServerWindows()` is the same machine
seen through `CGWindowListCopyWindowInfo`, which needs no grant.

**Diagnosis.** `w.Role() (role, subrole string, err error)` and
`w.Attributes() ([]string, error)`. An element that answers AXError **-25205**
— *the element does not have that attribute* — will happily list the attributes
it DOES have, and name its own role. Without that, a failed read of
`kAXPosition` is a dead end: nothing in the error says whether the element is a
window at all. That gap cost an hour of guessing once, and it is what these two
close. Measured immediately: `kAXWindows` of **Finder** returns the desktop as
an **`AXScrollArea`**, not a window — so an element in that list is not
guaranteed to be movable, and a caller that cares should read the role rather
than assume.

**Moving.** `Move(w, rect, opts)` and `MoveToDisplay(w, display, displays, opts)`.
The target is a **rectangle in global coordinates**, so the caller — which knows
what a ribbon position means — decides, and this package does not have to.

**Placement** is `Relative` (keep the position and proportion the window had, the
default), `Origin`, `Center` or `Fill`, with an `Inset` and an opt-out from
clamping. Note that `Relative` scales the size proportionally, which is a no-op
between equal-sized ribbon panels and a real shrink between a 7680×2160 display
and a 1920×1200 one; use `Center` or `Origin` to keep the size.

**`DisplayFor` decides by overlap area, not by the window's origin.** A window
straddling a seam has its origin on exactly one display, and it is routinely the
one showing the smaller sliver. Answering with that one would compute the
relative position against the wrong display and put the window somewhere nobody
asked for.

### The seam

`Move` speaks only to a `Window` interface — `Frame`, `SetPosition`, `SetSize`,
`Raise` — exactly as `go-macos/hotkey`'s `Resolve` speaks only to a `Registrar`.
The whole placement policy, **including the read-back check that decides whether
a window really moved**, is therefore tested to the last branch on Linux with no
AX anywhere in sight. That is where the negative control lives that no Mac is
needed for: a fake window that accepts every write and does not move, which is
precisely what AX hands you for a pinned window.

## Verification

**The move is proved by two instruments, neither of which performed the write.**
`TestLiveMoveIsProvedByTwoIndependentInstruments` opens a **fresh TextEdit
instance of its own** (never a window that was already on screen), moves it, and
then re-reads it through a **brand-new `AXUIElement`** — new
`AXUIElementCreateApplication`, new `kAXWindowsAttribute` copy, new element —
**and** through `CGWindowListCopyWindowInfo`, which is the window server's own
record and has no connection to AX at all. The two must agree with the target and
with each other.

```
before: AX 4915,155 603x505 | window server 4915,155 603x505
Move reported: 4915,155 603x505 → wanted 137,211 640x480, got 137,211 640x480
after : AX(fresh element) 137,211 640x480 | window server 137,211 640x480
PROVED: the window is at 137,211 640x480, confirmed by a fresh AXUIElement AND
by CGWindowListCopyWindowInfo, which had nothing to do with the write
```

**The negative control is a parameter, not a comment.** `proveMove` takes the
write as a boolean, and `TestLiveNegativeControlTheProofFailsWithoutTheWrite`
runs the identical measurement with it off. It **must** report "not moved"; if it
did not, the test above would be measuring something other than the write.

```
NEGATIVE CONTROL: the write is skipped; everything else is identical
after : AX(fresh element) 4915,155 603x505 | window server 4915,155 603x505
CONTROL HELD: with the write removed the window is still at 4915,155 603x505,
not 137,211 640x480, so the assertion in the test above is really testing the write
```

**The no-op detector is proved on a real window too.** Asked to move half a point
with the tolerance set to zero — a target the window server, which works in whole
points, cannot reach — `Move` reports the refusal instead of the `kAXErrorSuccess`
it was handed.

**And a move across displays is judged by where the window server puts it**, not
by a number: the assertion is that the window's centre lies inside the target
display's bounds and that `DisplayFor` attributes it there.

```
moving from display 4 [3840,0 7680x2160] to display 2 [0,0 1920x1200] (main)
PROVED: 268,86 151x281 is on display 2 [0,0 1920x1200] (main), per the window server
```

**Raising is proved through a third instrument.** Another application is brought
to the front first, and "frontmost" afterwards is read from the system-wide
`AXUIElement`, not from NSWorkspace's cache.

**A refusal is a skip, never a failure.** Where Accessibility has not been
granted, the live suite says exactly what is missing, what would grant it and to
whom, and gets out of the way. It is measuring the machine, not the package.

**No screen capture is taken anywhere in this repository**, so no artefact of a
person's desktop can be committed. The instruments are geometry, not pixels.

```bash
# The portable suite: no AX, no display, no permission. Runs anywhere.
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 GOOS=linux go test ./...

# The live suite. It opens a TextEdit instance of its own, moves that, and
# kills it. It never touches a window that was already on screen.
ACCESSIBILITY_INTEGRATION=1 go test -tags integration -v -run TestLive .

# The one test that is allowed to show the system permission dialog.
ACCESSIBILITY_INTEGRATION=1 ACCESSIBILITY_PROMPT=1 go test -tags integration -run TestLivePrompt .
```

The portable layer is at **100% statement coverage**, gated in CI on the darwin
lane; off darwin, where nothing exists but that layer and the stubs, **the whole
package is at 100%**, gated on the linux lane. The policy is *run*, not merely
compiled, on all six of Go's 64-bit architectures.

## The tool

```bash
go run ./cmd/axmove -trust                          # the trust state, and what to do about it
go run ./cmd/axmove -displays                       # the displays, in global coordinates
go run ./cmd/axmove -list                           # every window, with its display
go run ./cmd/axmove -focused -display 5             # move the window you are looking at
go run ./cmd/axmove -focused -rect 100,100,800,600  # or to an explicit rectangle
```

`axmove` never shows the permission dialog unless `-prompt` is given.

## Platforms

macOS only. Every other platform **compiles** and reports `ErrUnsupported`, so
consumers cross-compile without a build tag of their own — verified on
linux/{amd64,arm64,riscv64,loong64,ppc64le,s390x}, windows/{amd64,arm64},
darwin/{amd64,arm64} and freebsd/amd64. The whole placement policy works on all
of them, which is what makes it testable off a Mac.

## Licence

BSD-3-Clause.
