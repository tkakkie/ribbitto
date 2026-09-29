package web

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/channel"
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

type channelPages struct {
	pages   *pageRenderer
	service ChannelService
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
	p.render(w, r, m, c, http.StatusOK, "", "")
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
	p.render(w, r, m, c, http.StatusUnprocessableEntity, name, message)
}

func (p channelPages) render(w http.ResponseWriter, r *http.Request, m authz.Membership, c domain.Channel, status int, name, message string) {
	channels, err := p.service.List(r.Context(), m)
	if err != nil {
		serverError(w, r, "listing channels", err)
		return
	}
	account, _ := middleware.Account(r.Context())
	p.pages.render(w, r, status, func(url string) templ.Component {
		return view.Channel(url, view.ChannelPage{
			Organization: m.Organization, DisplayName: account.DisplayName, Handle: m.Member.Handle, Role: string(m.Member.Role),
			Current: c, Channels: channels, Name: name, Error: message,
		})
	})
}
