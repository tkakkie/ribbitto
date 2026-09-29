package web

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/channel"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// ChannelService provides channels within the resolved member's organisation.
type ChannelService interface {
	List(context.Context, authz.Membership) ([]domain.Channel, error)
	Get(context.Context, authz.Membership, domain.ID) (domain.Channel, error)
	Default(context.Context, authz.Membership) (domain.Channel, error)
	Create(context.Context, authz.Membership, string) (domain.Channel, error)
}

// MessageReader provides a page of the conversation with author names.
type MessageReader interface {
	Before(context.Context, authz.Membership, domain.ID, *int64) (message.Page, error)
}

type channelPages struct {
	messages MessageReader
	posting  *message.Service
	pages    *pageRenderer
	service  ChannelService
}

func (p channelPages) home(w http.ResponseWriter, r *http.Request, m authz.Membership) {
	c, err := p.service.Default(r.Context(), m)
	if err != nil {
		serverError(w, r, "finding default channel", err)
		return
	}
	http.Redirect(w, r, view.ChannelURL(m.Organization.Slug, c.ID), http.StatusSeeOther)
}

func (p channelPages) show(w http.ResponseWriter, r *http.Request, m authz.Membership) {
	raw := r.PathValue("channelID")
	var id domain.ID
	if len(raw) != 36 || raw[8] != '-' || raw[13] != '-' || raw[18] != '-' || raw[23] != '-' {
		http.NotFound(w, r)
		return
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(raw, "-", ""))
	if err != nil || len(decoded) != len(id) {
		http.NotFound(w, r)
		return
	}
	copy(id[:], decoded)
	c, err := p.service.Get(r.Context(), m, id)
	if errors.Is(err, channel.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, r, "finding channel", err)
		return
	}
	if r.Method == http.MethodPost {
		p.post(w, r, m, c)
		return
	}
	page := view.ChannelPage{}
	// ParseQuery, unlike URL.Query, reports malformed pairs instead of
	// dropping them, so a broken link cannot fall back to the latest page.
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if raw, ok := query["before"]; ok {
		// An event_seq is positive; anything else is a malformed link.
		before, err := strconv.ParseInt(raw[0], 10, 64)
		if len(raw) != 1 || err != nil || before < 1 {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		page.Before = before
	}
	p.render(w, r, m, c, http.StatusOK, page)
}

func (p channelPages) create(w http.ResponseWriter, r *http.Request, m authz.Membership) {
	if !parseForm(w, r) {
		return
	}
	name := r.PostForm.Get("name")
	c, err := p.service.Create(r.Context(), m, name)
	message := ""
	switch {
	case err == nil:
		http.Redirect(w, r, view.ChannelURL(m.Organization.Slug, c.ID), http.StatusSeeOther)
		return
	case errors.Is(err, channel.ErrInvalidName):
		message = "channel.error.name"
	case errors.Is(err, channel.ErrNameTaken):
		message = "channel.error.name_taken"
	default:
		serverError(w, r, "creating channel", err)
		return
	}
	// No channel identity is accepted from the form: failed creation returns
	// to the default conversation within the resolved organisation.
	c, err = p.service.Default(r.Context(), m)
	if err != nil {
		serverError(w, r, "finding default channel", err)
		return
	}
	p.render(w, r, m, c, http.StatusUnprocessableEntity, view.ChannelPage{Name: name, Error: message})
}

func (p channelPages) render(w http.ResponseWriter, r *http.Request, m authz.Membership, c domain.Channel, status int, page view.ChannelPage) {
	channels, err := p.service.List(r.Context(), m)
	if err != nil {
		serverError(w, r, "listing channels", err)
		return
	}
	var before *int64
	if page.Before > 0 {
		before = &page.Before
	}
	history, err := p.messages.Before(r.Context(), m, c.ID, before)
	if err != nil {
		serverError(w, r, "listing messages", err)
		return
	}
	account, _ := middleware.Account(r.Context())
	p.pages.render(w, r, status, func(url string) templ.Component {
		page.Organization, page.DisplayName, page.Handle, page.Role = m.Organization, account.DisplayName, m.Member.Handle, string(m.Member.Role)
		page.Current, page.Channels, page.Messages, page.Older = c, channels, history.Entries, history.Older
		return view.Channel(url, page)
	})
}

func (p channelPages) post(w http.ResponseWriter, r *http.Request, m authz.Membership, c domain.Channel) {
	if !parseForm(w, r) {
		return
	}
	body := r.PostForm.Get("body")
	_, err := p.posting.Post(r.Context(), m, c.ID, body)
	switch {
	case errors.Is(err, message.ErrInvalidBody):
		p.render(w, r, m, c, http.StatusUnprocessableEntity, view.ChannelPage{Body: body, BodyError: "message.error.body"})
	case errors.Is(err, channel.ErrNotFound), errors.Is(err, authz.ErrNotFound):
		// The channel, membership or organisation went away after this
		// request resolved them; answer as for a non-member.
		http.NotFound(w, r)
	case err != nil:
		serverError(w, r, "posting message", err)
	case r.Header.Get("HX-Request") == "true":
		p.render(w, r, m, c, http.StatusOK, view.ChannelPage{})
	default:
		http.Redirect(w, r, view.ChannelURL(m.Organization.Slug, c.ID), http.StatusSeeOther)
	}
}
