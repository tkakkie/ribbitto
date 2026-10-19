package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Each predicate has its own scenario/server/limiter. No case makes more than
// one deliberate composer POST; fixtures and stream delivery bypass HTTP limits.
func testQuoteSelectionBrowser(t *testing.T, browser *rod.Browser, width int, scenario string, fixture *quoteSelectionFixture) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID, Slug: "acme"}, Member: org.Member{ID: f.MemberID}}
	sessions := identitypg.NewSessions(pool, time.Now, nil)
	services := postgresServices(t, pool, sessions, "", false)
	cookie, _, err := sessions.Create(ctx, f.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	post := func(send conversation.Send) conversation.Message {
		t.Helper()
		sent, err := services.Posting.Send(ctx, m, f.Channel.ID, send)
		if err != nil {
			t.Fatal(err)
		}
		return sent.Message
	}
	first := post(conversation.Send{Body: "page original"})
	if fixture != nil {
		entry := fixture.entry(f.MemberID)
		// Plain-text legacy bodies cannot be created through today's posting API.
		if _, err := pool.Exec(ctx, "UPDATE message SET body = $2, format = $3 WHERE id = $1", first.ID, entry.Body, string(entry.Format)); err != nil {
			t.Fatal(err)
		}
	}
	second := post(conversation.Send{Body: "another original"})
	quoted := post(conversation.Send{Body: "before\n\n> nested quote\n\nafter", Quote: &first.ID})
	catalogues, err := i18n.New(slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, services)
	if err != nil {
		t.Fatal(err)
	}
	frames := make(chan realtime.Outgoing, 2)
	requests := make(chan url.Values, 4)
	sentPosts := make(chan url.Values, 1)
	var posts atomic.Int32
	feed := view.ChannelURL("acme", f.Channel.ID)
	emit := func(message conversation.Message) {
		t.Helper()
		entry, err := services.Messages.One(ctx, m, f.Channel.ID, message.EventSeq)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case frames <- realtime.Outgoing{ID: message.EventSeq, Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(viewMessage("acme", entry)))}:
		case <-ctx.Done():
			t.Fatal("stream fixture: frame could not be queued")
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/organizations/acme/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
			if sender.Heartbeat(r.Context()) != nil {
				return
			}
			for {
				select {
				case frame := <-frames:
					if sender.Send(r.Context(), frame) != nil {
						return
					}
				case <-r.Context().Done():
					return
				}
			}
		}
		if r.Method == http.MethodPost && r.URL.Path == feed+"/quote" {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			select {
			case requests <- r.PostForm:
			case <-r.Context().Done():
				return
			}
		}
		if r.Method == http.MethodPost && r.URL.Path == feed {
			posts.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			select {
			case sentPosts <- r.PostForm:
			case <-r.Context().Done():
				return
			}
		}
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
		handler.ServeHTTP(w, r)
		if r.Method == http.MethodPost && r.URL.Path == feed {
			history, err := services.Messages.Page(ctx, m, f.Channel.ID, nil, nil)
			if err != nil {
				t.Error(err)
				return
			}
			message := history.Entries[len(history.Entries)-1]
			select {
			case frames <- realtime.Outgoing{ID: message.EventSeq, Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(viewMessage("acme", message)))}:
			case <-r.Context().Done():
			}
		}
	}))
	defer server.Close()
	page, closePage := boundedPage(t, browser, ctx, "selection page")
	defer closePage()
	step := pageStep(ctx, page)
	step(t, "selection setup: viewport and touch emulation installed", func(p *rod.Page) {
		p.MustSetViewport(width, 844, 1, width < 768)
		if err := (proto.EmulationSetTouchEmulationEnabled{Enabled: width < 768}).Call(p); err != nil {
			panic(err)
		}
	})
	step(t, "selection navigation: page originals visible and all selection buttons hidden", func(p *rod.Page) {
		if err := navigateLoaded(p, server.URL+feed); err != nil {
			panic(err)
		}
		p.MustElement("#body-" + view.MessageDOMID(first.ID)).MustWaitVisible()
		p.MustWait(`() => document.querySelectorAll('[data-quote-selection]').length === 3 &&
   [...document.querySelectorAll('[data-quote-selection]')].every(b => b.hidden)`)
	})
	if scenario == "live" {
		first = post(conversation.Send{Body: "live original"})
		emit(first)
		step(t, "selection live delivery: new body and its hidden button arrived", func(p *rod.Page) {
			p.MustElement("#body-" + view.MessageDOMID(first.ID)).MustWaitVisible()
			p.MustWait(`id => document.getElementById(id).querySelector('[data-quote-selection]')?.hidden`, view.MessageDOMID(first.ID))
		})
	}
	body := "#body-" + view.MessageDOMID(first.ID)
	button := "#" + view.MessageDOMID(first.ID) + " > [data-quote-selection]"
	// Range changes alone must deliver selectionchange; never dispatch it manually.
	selectText := func(p *rod.Page, start, end string, from, to int) {
		p.MustEval(`(start,end,from,to) => {
   const text = selector => { const w = document.createTreeWalker(document.querySelector(selector), NodeFilter.SHOW_TEXT); return w.nextNode(); };
   const range = document.createRange(); range.setStart(text(start),from); range.setEnd(text(end),to);
   const selection = document.getSelection(); selection.removeAllRanges(); selection.addRange(range);
  }`, start, end, from, to)
	}
	show := func(p *rod.Page, selector string) {
		p.MustWait(`s => {
   const buttons = [...document.querySelectorAll('[data-quote-selection]')];
   return buttons.filter(b => !b.hidden).length === 1 && !document.querySelector(s).hidden;
  }`, selector)
	}
	step(t, "selection body: only this item's button appears", func(p *rod.Page) {
		if fixture != nil {
			p.MustEval(`s => {
    const range = document.createRange(); range.selectNodeContents(document.querySelector(s));
    const selection = document.getSelection(); selection.removeAllRanges(); selection.addRange(range);
   }`, body)
		} else {
			selectText(p, body, body, 5, 13)
		}
		show(p, button)
		if width < 768 {
			if !p.MustEval(`s => { const r = document.querySelector(s).getBoundingClientRect(); return r.width >= 44 && r.height >= 44; }`, button).Bool() {
				panic("selection target smaller than 44 x 44")
			}
		}
	})
	hidden := func(p *rod.Page) {
		p.MustWait(`() => [...document.querySelectorAll('[data-quote-selection]')].every(b => b.hidden)`)
	}
	switch scenario {
	case "partial-mention":
		step(t, "selection starts inside display name: no button", func(p *rod.Page) {
			p.MustEval(`s => {
    const body = document.querySelector(s), mention = body.querySelector('[data-member-id]');
    const range = document.createRange(); range.setStart(mention.querySelector('bdi').firstChild, 1);
    range.setEnd(body.lastElementChild.lastChild, 3);
    const selection = document.getSelection(); selection.removeAllRanges(); selection.addRange(range);
   }`, body)
			hidden(p)
		})
		return
	case "backward":
		step(t, "backward selection: end anchor produces the same part", func(p *rod.Page) {
			p.MustEval(`s => { const node = document.querySelector(s).firstElementChild.firstChild;
    document.getSelection().setBaseAndExtent(node, 13, node, 5); }`, body)
			show(p, button)
		})
	case "triple-click":
		step(t, "triple-click selection: empty outside tail remains quotable", func(p *rod.Page) {
			p.MustEval(`s => {
    const body = document.querySelector(s), range = document.createRange();
    range.setStart(body, 0); range.setEnd(body.nextElementSibling, 0);
    const selection = document.getSelection(); selection.removeAllRanges(); selection.addRange(range);
   }`, body)
			show(p, button)
		})
	case "across":
		step(t, "selection across messages: neither endpoint's button is shown", func(p *rod.Page) {
			selectText(p, body, "#body-"+view.MessageDOMID(second.ID), 5, 7)
			hidden(p)
		})
		return
	case "quote":
		step(t, "selection touching snapshot: body plus quoted original shows no button", func(p *rod.Page) {
			selectText(p, "#"+view.MessageDOMID(quoted.ID)+" figure blockquote", "#body-"+view.MessageDOMID(quoted.ID)+" > p", 0, 4)
			hidden(p)
		})
		return
	case "nested-quote":
		step(t, "selection inside rich body: crossing its quote block shows no button", func(p *rod.Page) {
			selectText(p, "#body-"+view.MessageDOMID(quoted.ID)+" > p:first-child", "#body-"+view.MessageDOMID(quoted.ID)+" > p:last-child", 0, 5)
			hidden(p)
		})
		return
	case "empty":
		step(t, "selection whitespace: non-collapsed space shows no button", func(p *rod.Page) {
			selectText(p, body, body, 4, 5)
			hidden(p)
		})
		step(t, "selection collapsed: caret shows no button", func(p *rod.Page) {
			selectText(p, body, body, 5, 5)
			hidden(p)
		})
		return
	case "transfer":
		step(t, "selection transfer: old button hides and only the new button appears", func(p *rod.Page) {
			target := "#body-" + view.MessageDOMID(second.ID)
			selectText(p, target, target, 8, 16)
			show(p, "#"+view.MessageDOMID(second.ID)+" > [data-quote-selection]")
		})
		return
	case "lost":
		step(t, "selection lost at submit: cancel instead of creating a whole chip", func(p *rod.Page) {
			p.MustEval(`s => { const b = document.querySelector(s); document.getSelection().removeAllRanges();
    b.form.requestSubmit(b); }`, button)
			hidden(p)
			// A sentinel request runs after the attempted submit; its response proves
			// htmx processing has finished, rather than passing on an early absence.
			p.MustEval(`() => document.getElementById('quote-form').requestSubmit()`)
			p.MustWait(`() => !document.getElementById('quote-form').classList.contains('htmx-request') && document.getElementById('composer-quote').hidden`)
		})
		select {
		case form := <-requests:
			if form.Has("quote") || form.Has("quote_text") {
				t.Fatalf("lost selection unexpectedly sent %v", form)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("lost selection: sentinel request missing")
		}
		select {
		case <-requests:
			t.Fatal("lost selection sent an extra request")
		default:
		}
		return
	case "composer":
		step(t, "composer submit after selection: input remains absent", func(p *rod.Page) {
			p.MustElement("#message-body").MustInput("unquoted selection answer")
			p.MustElement(`#message-composer button[type=submit]:not([form])`).MustClick()
			p.MustWait(`() => [...document.querySelectorAll('#message-items [id^="body-message-"]')].some(b => b.textContent === 'unquoted selection answer')`)
		})
		select {
		case form := <-sentPosts:
			if form.Has("quote_text") {
				t.Fatal("unquoted composer carried quote_text")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("composer submit: request missing")
		}
		return
	case "menu", "part":
		step(t, "menu Quote after selection: whole chip has no part textarea", func(p *rod.Page) {
			p.MustElement(fmt.Sprintf(`[popovertarget="message-actions-%x"]`, first.ID)).MustClick()
			p.MustElement(fmt.Sprintf(`#message-actions-%x [name=quote]`, first.ID)).MustClick()
			p.MustWait(`() => document.querySelector('#composer-quote [name=part]') && !document.getElementById('quote-text')`)
		})
		select {
		case form := <-requests:
			if form.Has("quote_text") {
				t.Fatal("menu Quote carried quote_text")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("menu Quote: request missing")
		}
		if scenario == "part" {
			step(t, "Quote only part after selection: original fills the editor", func(p *rod.Page) {
				p.MustElement(`#composer-quote [name=part]`).MustClick()
				p.MustWait(`() => document.getElementById('quote-text')?.value === 'page original'`)
			})
			select {
			case form := <-requests:
				if form.Has("quote_text") {
					t.Fatal("Quote only part carried selection quote_text")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Quote only part: request missing")
			}
		}
		return
	}
	want := "original"
	if fixture != nil {
		want = fixture.quoteText
	}
	if scenario == "triple-click" {
		want = "page original"
	}
	if scenario == "changed" {
		want = "page"
		step(t, "selection changed at submit: use live text before selectionchange delivery", func(p *rod.Page) {
			p.MustEval(`s => {
    const b = document.querySelector(s), w = document.createTreeWalker(b.previousElementSibling, NodeFilter.SHOW_TEXT);
    const node = w.nextNode(), range = document.createRange(); range.setStart(node,0); range.setEnd(node,4);
    const selection = document.getSelection(); selection.removeAllRanges(); selection.addRange(range);
    b.form.requestSubmit(b);
   }`, button)
			p.MustWait(`() => document.getElementById('quote-text')?.value === 'page'`)
		})
	}
	step(t, "selection press: native pointer activation preserves text and creates editable chip", func(p *rod.Page) {
		if scenario != "changed" {
			if width < 768 {
				p.MustElement(button).MustTap()
			} else {
				p.MustElement(button).MustClick()
			}
		}
		p.MustWait(`want => document.getElementById('quote-text')?.value === want`, want)
		p.MustWait(`() => { const i = document.querySelector('#quote-form input[name=quote_text]'); return i.disabled && i.value === ''; }`)
	})
	if scenario == "page" {
		step(t, "selection focus: editable part takes focus", func(p *rod.Page) {
			p.MustWait(`() => document.activeElement.id === 'quote-text'`)
		})
	}
	select {
	case form := <-requests:
		if form.Get("quote") != realtime.FormatPayloadID(first.ID) || len(form["quote_text"]) != 1 || form.Get("quote_text") != want {
			t.Fatalf("selection request: %v", form)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("selection chip: request missing")
	}
	step(t, "selection compose: unique answer entered", func(p *rod.Page) {
		p.MustElement("#message-body").MustInput("selection answer")
		p.MustWait(`() => document.getElementById('message-body').value === 'selection answer'`)
	})
	step(t, "selection send: posted message shows exactly the part, author and original link", func(p *rod.Page) {
		p.MustElement(`#message-composer button[type=submit]:not([form])`).MustClick()
		p.MustWait(`(href,want) => [...document.querySelectorAll('#message-items > li')].some(item =>
   item.querySelector('[id^="body-message-"]')?.textContent === 'selection answer' &&
   item.querySelector('figure blockquote')?.textContent === want &&
   item.querySelector('figure figcaption')?.textContent.includes('@owner') &&
   item.querySelector('figure a')?.getAttribute('href') === href)`, view.MessageURL("acme", first.ID), want)
	})
	if scenario == "selection-menu" {
		step(t, "menu after quoted post: another message's actions open", func(p *rod.Page) {
			p.MustElement(fmt.Sprintf(`[popovertarget="message-actions-%x"]`, second.ID)).MustClick()
			p.MustElement(fmt.Sprintf(`#message-actions-%x [name=quote]`, second.ID)).MustWaitVisible()
		})
		step(t, "menu after quoted post: whole Quote creates another message's whole chip", func(p *rod.Page) {
			p.MustElement(fmt.Sprintf(`#message-actions-%x [name=quote]`, second.ID)).MustClick()
			p.MustWait(`id => document.querySelector('#composer-quote [name=part]')?.value === id &&
   document.querySelector('#composer-quote input[name=quote]')?.value === id && !document.getElementById('quote-text')`, realtime.FormatPayloadID(second.ID))
		})
		select {
		case form := <-requests:
			if form.Get("quote") != realtime.FormatPayloadID(second.ID) || form.Has("quote_text") {
				t.Fatalf("menu after quoted post: unexpected second chip request %v", form)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("menu after quoted post: second chip request missing")
		}
	}
	if posts.Load() != 1 {
		t.Fatalf("selection send: posts = %d, want one deliberate send", posts.Load())
	}
}
