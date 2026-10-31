# Forms and tap targets

Form, error and tap-target rules for [`accessibility.md`](accessibility.md).
`TestPagesMarkup` (`internal/web/markup_test.go`) enforces the ✓ rules.

- **Forms are labelled.** ✓ Every text input, `<select>` and `<textarea>` has
  a `<label>` (wrapping or `for`/`id`); hidden inputs need none.
  Field labels contain only the field name; errors sit outside the label,
  linked from the input with `aria-describedby`, and use `role="alert"`
  after a submit. Setup and sign-up use the shared `textField` component:
  ✓ invalid inputs have `aria-invalid="true"` and an error description;
  valid inputs omit both attributes. Sign-in uses `textField` with empty
  field errors and keeps one form-level alert. A radio group's error (the
  channel image picker) is its own alert: every radio in the group has
  `aria-invalid="true"` and is described by it. Enhanced failures fill
  `#message-send-alert` (`role="alert"`, outside swaps) with localised
  not-sent, unconfirmed or recovered text. Pending failures
  use an empty polite status; queued/sending states are silent.
  Retry/Edit/Remove describe their unique body. With composer text, Edit is
  disabled; `aria-describedby` also names its visible localised explanation.
  Edit focuses the restored text's end. At 10 items,
  a localised capacity alert retains the draft, `aria-invalid` and
  `aria-describedby`.
- **Tap targets** at phone width are at least 44 × 44 px, stricter than
  AA's 24 px (2.5.8), which desktop keeps: links, buttons, form controls,
  `<summary>` and labels that act as a control's target. The exceptions
  are links inside running text (message and help text) and a control's
  caption label while the control itself is a 44 × 44 px target
  ([`ui.md`](ui.md#layout-principles), #966, #974). Welcome text fields and
  buttons have a 48 px minimum height below `desk`, and every text input,
  textarea and select has at least 16 px text there (#973, #1120). `TestBrowser/width-audit`
  (`internal/web/width_audit_browser_test.go`) checks these targets, control
  reachability, and document and scroll-container horizontal overflow on
  every page at phone, landscape and breakpoint widths (#974). A target
  label is a checkbox's or radio's label, which stands in for the small
  native widget; another control's caption is exempt only while that
  control is itself a 44 × 44 px target.

The move form (`#branch-form`) shows only while a message is checked (#923),
through CSS `:has()` alone, so it behaves the same without JavaScript. Until
then it is `display: none`: out of the tab order and the accessibility tree.
Checking a message never moves focus; the form appears next in the tab order
after the list, over the composer's area, which turns invisible but keeps its
space, so the list neither resizes nor scrolls. A plain failed submit, which
checks nothing, keeps the form and its alert in the column above the composer;
a browser without `:has()` shows both, as before. Below the
[short height](ui-panels-and-motion.md#short-heights) the form follows the list in
the column instead, and the composer is hidden.

Keyboard/screen-reader quoting: menu → Quote → Quote only part → labelled editor
→ send, without scripts/selection. Swaps focus Remove (Tab → Quote only part),
part editor, or body on removal; 422/409 keep focus/caret.
[Focus/autofocus rules](architecture/web-composer.md#composer).
Chip actions have 44 px targets. "Quote selection" follows the body,
hidden from focus/accessibility unless selection stays in one body; its target
is 44 × 44 px below `desk`. Pressing preserves selection.
