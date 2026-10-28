// Package main is a temporary demonstration, never merged: it calls a
// function with a known vulnerability in an old golang.org/x/text.
package main

import "golang.org/x/text/language"

func main() {
	_, _, _ = language.ParseAcceptLanguage("en")
}
