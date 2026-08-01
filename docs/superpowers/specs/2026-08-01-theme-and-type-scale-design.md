# Theme switching and a type scale

Date: 2026-08-01
Status: shipped, #58 and #59

Three things the implementation learned that this document did not know:

- A row with an explanation in it grows past `--row-h` at large sizes, because
  the "why" cell wraps. Correct behaviour, and it means a row-height assertion
  has to measure a row that cannot wrap.
- At 1.6 a dense table is wider than the window and scrolls sideways. Every
  column stays reachable and the window itself never scrolls, both pinned by
  tests. Whether horizontal scroll is the right answer is still open.
- The product has ten distinct type sizes, not the seven the foundation
  permits. `9px`, `9.5px`, `14px` and `20px` are outside the scale, and the
  first two differ by half a pixel. They kept their shipped values, because
  changing them is a design decision and this was a plumbing one.

Two settings a developer can change: which theme the product uses, and how large
its text is. Reachable from the command palette and from the keyboard, and from
nowhere else.

## Why this is not one feature

They arrive together in a request and split cleanly in the code.

The light theme already exists. `[data-theme="light"]` defines every token in
`web/src/tokens.css`, ported from the Claude Design project, and every rule
below the theme blocks uses those tokens with no hardcoded colour anywhere.
`index.html` pins `data-theme="dark"` and nothing can change it. So the theme
work is a switch, not a design.

The type scale does not exist at all. The seven sizes the design system names
are written as literal pixels in 44 places in `tokens.css` and 39 inline
`fontSize:` props across the components. That work is mechanical and touches
most of the UI.

One issue each. Theme first, because it is smaller and the tokens are already
there.

## Why not browser zoom

The browser already scales text, and it scales everything else with it: padding,
rail width, row spacing. At 1.25x you lose roughly a fifth of your rows.

`docs/03-product-spec.md` and the design system's typography foundation both say
the same thing in different words. The foundation is blunt about it:

> Seven sizes, no more. A dense instrument earns hierarchy from weight, case,
> and color rather than from size, because large type would cost rows on screen
> and rows are the product.

So this scale is deliberately narrower than zoom. **Type and row height grow.
The 4px spacing grid holds.** That is the thing browser zoom cannot do, and it
is the only reason this is worth building rather than documenting `⌘+`.

The two compose. Zoom the window, scale the type inside it.

## The scale

```css
:root {
  --scale: 1;                              /* set from the stored preference */
  --fs-title: calc(26px   * var(--scale));
  --fs-head:  calc(19px   * var(--scale));
  --fs-body:  calc(13px   * var(--scale));
  --fs-data:  calc(12px   * var(--scale));
  --fs-log:   calc(11.5px * var(--scale));
  --fs-label: calc(11px   * var(--scale));
  --fs-col:   calc(10px   * var(--scale));
  --row-h:    calc(34px   * var(--scale));
}
```

Seven tokens for the seven sizes the foundation names. No eighth size is
introduced; this makes the existing scale addressable, it does not extend it.

Steps: `0.9 · 1.0 · 1.1 · 1.25 · 1.4 · 1.6`. Discrete, so a keypress has a
predictable feel and nobody lands on 1.037. Default 1.0.

A few fixed widths hold text and clip if the text grows without them:

| Value | Holds |
| --- | --- |
| `--rail-w` 216px | Environment names |
| `.log .pod` 132px | Pod names in the log gutter |
| `.tl-lane .lane-label` 96px, `.tl-ticks` margin | Timeline lane labels |
| `.blast dt/dd` 112px | Blast-radius terms |

Those scale. `--s1` through `--s8`, the radii, and the dialog and palette widths
do not.

## Keyboard

`⌘+` and `⌘-` are browser zoom. Taking them over would remove a control the
developer already has in order to replace it with a similar one, and would break
the composition above.

| Binding | Does |
| --- | --- |
| `⌘⇧L` | Cycle theme: system → light → dark → system |
| `⌘⌥+` | One step larger |
| `⌘⌥-` | One step smaller |
| `⌘⌥0` | Back to 1.0 |

Every one of them is also a palette command, under a `Settings` group, because
`feature:palette` says every action is reachable from the keyboard and the
palette is how that promise is kept.

## State

`web/src/prefs.ts`, a pure module in the shape of `rows.ts` and `commands.ts`:

```ts
read(storage, prefersDark): Prefs      // junk in storage reads as absent
apply(root, prefs, prefersDark): void  // sets data-theme and --scale
step(scale, direction): number         // walks the ladder, clamped
```

`theme` is `"system" | "dark" | "light"`. `"system"` is the default and keeps
tracking `prefers-color-scheme` live, so a desktop that flips at sunset takes
kubeside with it. Choosing dark or light pins it and stops the tracking.

Persisted in `localStorage`. The binary still writes nothing.
`docs/04-multi-cluster.md` scopes the disk guarantee to observed data: "no
history, no cache, no credentials, no telemetry. A config file the user chose to
create is a different thing." A preference the user chose is that different
thing, and it never leaves their browser.

`commands.ts` stays pure. A settings command carries an `action` string rather
than a closure, and the palette maps it to a handler, exactly as it already does
for routes.

## What breaks in light mode

`ExecScreen.tsx:55` hands xterm.js a hardcoded palette:

```ts
theme: { background: "#0A0E10", foreground: "#DEE7EA", cursor: "#DEE7EA" }
```

A shell would stay dark on a light screen. It reads the computed tokens instead
and re-reads when the theme changes. Its font size follows `--scale` too.

## Tests

Written first, in the order the code is.

- `prefs.test.ts`: junk in storage, an out-of-range scale, stepping off both
  ends of the ladder, system tracking versus a pinned theme.
- `commands.test.ts`: the settings commands exist and match what is typed.
- Playwright: the apps screen in light, its own baseline on both platforms. The
  suite currently pins `colorScheme: dark`, so light has never rendered in CI.
- Playwright: a scale change moves the row height, asserted as a number rather
  than left to the pixel gate, which tolerates 2592 changed pixels.

## Out of scope

No settings screen, no rail control, no topbar control. The design project
specifies none of these and this adds none. Anyone who wants a knob opens the
palette.

No per-environment or per-context themes. The environment channel already owns
colour, and a second source of it would fight the one that means something.

No font family choice. IBM Plex is the design system.

## Open question

`web/src/tokens.css` is a port of the Claude Design project's `tokens.css`. The
two have now diverged in four ways, none of them pushed back:

- `--scale` and the ten `--fs-*` tokens
- `--row-h` and four fixed widths expressed as `calc()`
- the `@media (prefers-color-scheme: light)` block the CSP made necessary
- the font `@import`, which points at `./fonts.css` rather than Google

Still undecided: push the layer back so the two stay in step, or let the repo
be the source of truth for the product and treat the design project as the
visual reference it was. Writing to a shared project needs a human to say so.
