package web

import (
	"fmt"

	"github.com/go-rod/rod"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/ysmood/gson"
)

// Keep message identity at the call site so a shared menu can resolve its
// opener, controls and stored pin state without changing the test scenarios.
type browserMessageMenu map[string]string

func messageMenu(id kernel.ID) browserMessageMenu {
	menu := "#" + view.MessageActionsID
	return browserMessageMenu{
		"menu": menu, "button": fmt.Sprintf("#message-actions-%x + button", id),
		"copy": menu + " a", "quote": menu + " button[form=quote-form][name=quote]",
		"form": "#pin-form-" + view.MessageDOMID(id),
		"pin":  "#pin-false-" + view.MessageDOMID(id), "unpin": "#pin-true-" + view.MessageDOMID(id),
	}
}

func popoverMenu(id string) browserMessageMenu {
	return browserMessageMenu{"menu": "#" + id, "button": `[popovertarget="` + id + `"]`}
}

const browserPopoverElement = `controls => document.querySelector(controls.menu)`
const browserPopoverInvoker = `controls => [...document.querySelectorAll(controls.button)].find(e => e.checkVisibility())`

func (m browserMessageMenu) selector(control string) string { return m[control] }

func (m browserMessageMenu) control(p *rod.Page, name string) *rod.Element {
	return p.MustElement(m.selector(name))
}

func (m browserMessageMenu) open(p *rod.Page) { m.control(p, "button").MustClick() }

func (m browserMessageMenu) show(p *rod.Page) {
	m.eval(p, `({menu, button, form}) => { if (!menu.matches(':popover-open') || !menu.contains(form)) button.click(); }`)
}

// Prepare Quote without scrolling an offscreen message into view: growth tests
// must keep their reader anchor while the composer chip changes.
func (m browserMessageMenu) submitQuote(p *rod.Page, quoted bool) {
	m.eval(p, `({button, menu, quote}, quoted) => {
		if (quoted) { button.click(); menu.hidePopover(); }
		document.getElementById('quote-form').requestSubmit(quoted ? quote : document.querySelector('#composer-quote button'));
	}`, quoted)
}

// Resolve controls on every evaluation: pin responses replace the form.
func (m browserMessageMenu) script(js string) string {
	return `(...args) => { const controls = {}; for (const [name, selector] of Object.entries(args.shift())) controls[name] = document.querySelector(selector); return (` + js + `)(controls, ...args); }`
}

func (m browserMessageMenu) args(args []any) []any {
	return append([]any{m}, args...)
}

func (m browserMessageMenu) eval(p *rod.Page, js string, args ...any) gson.JSON {
	return p.MustEval(m.script(js), m.args(args)...)
}

func (m browserMessageMenu) wait(p *rod.Page, js string, args ...any) {
	p.MustWait(m.script(js), m.args(args)...)
}

func (m browserMessageMenu) pinSelector(pinned bool) string {
	if pinned {
		return m.selector("unpin")
	}
	return m.selector("pin")
}

func (m browserMessageMenu) pinSource(pinned bool) string { return m.pinSelector(pinned)[1:] }
func (m browserMessageMenu) formID() string               { return m.selector("form")[1:] }

// The concurrency assertion includes both messages even when only A's menu
// is open: B's late response must preserve A's state and focused action.
func messagePinsIndependent(p *rod.Page, a, b kernel.ID) bool {
	return messageMenu(a).eval(p, `({menu, form, unpin}, b) => menu.contains(form) && form.dataset.pinned === 'true' && document.activeElement === unpin && document.querySelector(b).dataset.pinned === 'true' && !document.querySelector(b).checkVisibility()`, messageMenu(b).selector("form")).Bool()
}

func (m browserMessageMenu) waitPinFocus(p *rod.Page, pinned bool) {
	m.wait(p, `({form, pin, unpin}, state) => document.activeElement === (state === 'true' ? unpin : pin) && document.activeElement.form === form && form.dataset.pinned === state`, fmt.Sprint(pinned))
}
