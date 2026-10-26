package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const definitions = `package view
const (
 //dom:id HTMLTextAreaElement
 BodyID = "message-body"
 //dom:suffix HTMLLIElement
 ChannelDOMPrefix = "channel-"
 //dom:attribute
 Count = "data-unread-count"
 //dom:event Detail
 Changed = "sidebar-resync"
)
type Detail struct{}
`

func write(t *testing.T, root, path, source string) {
	t.Helper()
	path = filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLiteralForms(t *testing.T) {
	forms := []string{
		`document.getElementById("message-body")`, `document.getElementById('channel-0123456789abcdef0123456789abcdef')`,
		"document.getElementById(`message-body`,)",
		`document.getElementById("channel-" + key)`,
		`document.querySelector("#message-body > li")`, `el.closest('#channel-0123456789abcdef0123456789abcdef')`,
		"el.querySelector(`[data-unread-count=\"${n}\"]`)",
		"el.querySelector(`#channel-${id}`)",
		"el.querySelector(`li[data-x=\"${{ id }.id}-${n}\"] #message-body`)",
		`el.matches("[data-unread-count]")`, `el.matches('[data-unread-count="1"]')`,
		`el.dataset.unreadCount`, `el.dataset["unreadCount"]`, `dataset['unreadCount']`,
		`new Event("sidebar-resync")`, `new CustomEvent('sidebar-resync', {})`,
		`el.addEventListener("sidebar-resync", fn)`, `el.removeEventListener('sidebar-resync', fn)`,
		`htmx.trigger(el, "sidebar-resync")`, `htmx.trigger(find(a, b), 'sidebar-resync', {})`,
	}
	for _, method := range []string{"getAttribute", "setAttribute", "hasAttribute", "removeAttribute", "toggleAttribute"} {
		forms = append(forms, `el.`+method+`("data-unread-count", "1")`)
	}
	templates := []string{`<div id="message-body"/>`, `<li id="channel-0123456789abcdef0123456789abcdef"/>`, `<div data-unread-count={ count }/>`, `<div data-unread-count?={ b }/>`, `<div data-unread-count/>`, `<div data-unread-count hidden/>`, `<div { "data-unread-count" }={ count }/>`, `<div id={ "message-body" }/>`}
	for _, attr := range []string{"hx-target", "hx-select", "hx-select-oob", "hx-sync", "hx-trigger"} {
		value := "#message-body:replace"
		if attr == "hx-trigger" {
			value = "sidebar-resync from:body, submit"
		}
		templates = append(templates, `<div `+attr+`="`+value+`"/>`)
	}
	for _, template := range []bool{false, true} {
		cases, path := forms, "web/src/nested/consumer.ts"
		if template {
			cases, path = templates, "internal/web/view/example.templ"
		}
		for _, source := range cases {
			t.Run(source, func(t *testing.T) {
				root := t.TempDir()
				write(t, root, "internal/web/view/contracts.go", definitions)
				write(t, root, "web/src/contracts.gen.ts", forms[0])
				write(t, root, path, "\n\n"+source)
				problems, err := check(root)
				if err != nil || len(problems) != 1 || !strings.HasPrefix(problems[0], path+":3:") {
					t.Fatalf("want one file:line finding, got %v, %v", problems, err)
				}
			})
		}
	}
}

func TestScopeAndNonContracts(t *testing.T) {
	root := t.TempDir()
	write(t, root, "internal/web/view/contracts.go", definitions)
	write(t, root, "web/src/contracts.gen.ts", `document.getElementById("message-body")`)
	write(t, root, "web/static/legacy-v1.js", `new Event("sidebar-resync")`)
	write(t, root, "web/src/helper.ts", `import * as c from "./contracts.gen.ts";
c.getBody(); c.channelEntryID(key); c.readAttribute(el, c.Count); c.dispatchChanged(el, {});
document.getElementById("channel-create"); document.getElementById("message-body-x"); el.getAttribute("data-unread-count-extra");
el.dataset.unreadCountExtra; new Event("sidebar-resync-extra");
document.querySelector(".message-body [data-unread-count-extra] #message-body-x");
i18n("label.message-body"); el.classList.add("message-body");
// el.getElementById("message-body");
/* el.dataset.unreadCount; */
// Computed hooks are intentionally not proved by this text check.
const prefix = "chan" + "nel-"; document.getElementById(prefix + key); const event = "sidebar-" + "resync";
document.getElementById("message-body" + key);
`+"el.querySelector(`.message-body-${x} [data-unread-count-extra=\"${n}\"]`);\n"+
		"document.getElementById(`channel-${id}`);\n"+
		"el.querySelector(`[data-x=\"${{ hook: '#message-body' }.hook}\"]`);\n")
	write(t, root, "internal/web/view/example.templ", `<div id={ BodyID } { Count }={ count } hx-target={ "#" + BodyID } hx-trigger={ Changed }/>
<div id="message-body-x" class="message-body" data-unread-count-extra="1" hx-trigger="sidebar-resync-extra"/>
<p>data-unread-count is a contract attribute.</p>
func example() { id = "message-body" }
<!-- <div id="message-body"/> -->`)
	if problems, err := check(root); err != nil || len(problems) != 0 {
		t.Fatalf("unexpected findings: %v, %v", problems, err)
	}
	write(t, root, "web/src/helper.ts", "\nel.dataset.unreadCount;\nnew Event('sidebar-resync');")
	if problems, err := check(root); err != nil || len(problems) != 2 || !strings.HasPrefix(problems[0], "web/src/helper.ts:2:") || !strings.HasPrefix(problems[1], "web/src/helper.ts:3:") {
		t.Fatalf("hand-written sibling must report every finding: %v, %v", problems, err)
	}
	write(t, root, "internal/web/view/contracts.go", strings.ReplaceAll(definitions, "data-unread-count", "data-renamed"))
	if problems, err := check(root); err != nil || len(problems) != 1 {
		t.Fatalf("renamed Go definition must change the checked names: %v, %v", problems, err)
	}
}
