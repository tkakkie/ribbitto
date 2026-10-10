package web

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func (p channelPages) members(w http.ResponseWriter, r *http.Request, m org.Membership) {
	id, ok := channelID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	var after *kernel.ID
	if raw, present := query["after"]; present {
		parsed, valid := pathID(raw[0])
		if len(raw) != 1 || !valid {
			p.invalidQuery(w, r, m, id)
			return
		}
		after = &parsed
	}
	if err != nil {
		p.invalidQuery(w, r, m, id)
		return
	}
	result, err := p.messages.Members(r.Context(), m, id, after)
	if errors.Is(err, conversation.ErrChannelNotFound) || errors.Is(err, org.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, r, "reading members page", err)
		return
	}
	account, _ := middleware.Account(r.Context())
	page := view.ChannelPage{
		Organization: view.Organization{Slug: m.Organization.Slug, Name: m.Organization.Name},
		DisplayName:  memberDisplayName(account.DisplayName), Handle: m.Member.Handle, Role: string(m.Member.Role),
		Current: viewChannel(result.Current), Channels: viewChannels(result.Channels), Topics: viewTopics(result.Topics), EventCursor: result.EventCursor,
		Members: &view.MemberPage{Next: result.Next},
	}
	for _, member := range result.Members {
		page.Members.Items = append(page.Members.Items, view.Member{DisplayName: memberDisplayName(member.DisplayName), Handle: member.Handle})
	}
	p.pages.render(w, r, http.StatusOK, func(url string) templ.Component { return view.ChannelScreen(url, page) })
}
