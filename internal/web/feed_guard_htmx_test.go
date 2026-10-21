package web

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

const feedHTMXVerbs = "get post put delete patch"

func feedHas(n *html.Node, keys ...string) bool {
	for _, key := range keys {
		if _, ok := attrOK(n, key); ok {
			return true
		}
	}
	return false
}

func feedSubmit(n *html.Node) bool {
	// CSS HTML attribute values use ASCII case folding, not Unicode folding.
	value := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, attr(n, "type"))
	return value == "submit"
}

// feedHTMXSelected mirrors Ht; hx-on:* belongs to a separate pass without
// beforeProcessNode. The root body is counted separately by measureFeed.
func feedHTMXSelected(n *html.Node) bool {
	// x/net/html attaches template contents as children; browser selectors
	// cannot enter their separate DocumentFragment during body processing.
	for ancestor := n.Parent; ancestor != nil; ancestor = ancestor.Parent {
		if ancestor.Type == html.ElementNode && ancestor.Data == "template" {
			return false
		}
	}
	if n.Data == "form" || feedSubmit(n) || feedHas(n, "hx-ext", "data-hx-ext", "hx-trigger", "data-hx-trigger", "sse-connect", "data-sse-connect", "sse-swap", "data-sse-swap") {
		return true
	}
	for _, verb := range strings.Fields(feedHTMXVerbs) {
		if feedHas(n, "hx-"+verb, "data-hx-"+verb) {
			return true
		}
	}
	if n.Data == "a" {
		for ancestor := n; ancestor != nil; ancestor = ancestor.Parent {
			if feedHas(ancestor, "hx-boost", "data-hx-boost") {
				return true
			}
		}
	}
	return false
}

func TestFeedGuardHTMXPin(t *testing.T) {
	for file, pieces := range map[string][]string{
		"htmx-2.0.7.min.js": {
			`de=["` + strings.Join(strings.Fields(feedHTMXVerbs), `","`) + `"]`,
			`T=de.map(function(e){return"[hx-"+e+"], [data-hx-"+e+"]"}).join(", ")`,
			`", [hx-boost] a, [data-hx-boost] a, a[hx-boost], a[data-hx-boost]"`,
			`", form, [type='submit'],"+" [hx-ext], [data-hx-ext], [hx-trigger], [data-hx-trigger]"`,
			`disableSelector:"[hx-disable], [data-hx-disable]"`,
		},
		"htmx-ext-sse-2.2.4.min.js": {`getSelectors:function(){return["[sse-connect]","[data-sse-connect]","[sse-swap]","[data-sse-swap]"]}`},
	} {
		source, err := os.ReadFile("../../web/static/vendor/" + file)
		if err != nil {
			t.Fatal(err)
		}
		for _, piece := range pieces {
			if !strings.Contains(string(source), piece) {
				t.Errorf("%s missing %q: re-check the htmx selection mirror", file, piece)
			}
		}
	}
}

func TestFeedGuardHTMXSelection(t *testing.T) {
	for _, tt := range []struct {
		markup string
		want   int
	}{
		{`<form><button></button></form><button form="quote-form" name="quote"></button><button data-quote-selection></button>`, 1},
		{`<template hx-trigger><form><button type="submit"></button></form></template>`, 1},
		{`<div hx-boost><a></a><span></span></div><a data-hx-boost></a>`, 2},
		{`<form hx-post="/"><button type="SuBmIt"></button></form><i type="ſubmit"></i>`, 2},
		{`<i data-hx-patch></i><i data-hx-trigger></i><i data-sse-swap></i><i hx-on:click></i><i hx-swap></i>`, 3},
	} {
		doc, err := html.Parse(strings.NewReader(tt.markup))
		if err != nil {
			t.Fatal(err)
		}
		if got := countElements(doc, feedHTMXSelected); got != tt.want {
			t.Errorf("%s: selected %d, want %d", tt.markup, got, tt.want)
		}
	}
}
