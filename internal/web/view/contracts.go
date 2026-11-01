package view

//go:generate go -C ../../../tools run ./domcontract ..

// The dom directives describe script hooks without importing the application
// into tools. Element types describe server markup; modules validate data values.
const (
	// OrganizationStreamID identifies the shared stream container.
	//dom:id HTMLDivElement
	OrganizationStreamID = "organization-stream"
	// StreamSinkID identifies the out-of-band stream sink.
	//dom:id HTMLDivElement
	StreamSinkID = "stream-sink"
	// SidebarResyncID identifies the sidebar recovery form.
	//dom:id HTMLFormElement
	SidebarResyncID = "sidebar-resync"
	// SidebarChannelsID identifies the channel list snapshot.
	//dom:id HTMLUListElement
	SidebarChannelsID = "sidebar-channels"
	// SidebarTopicsID identifies the nested topic list.
	//dom:id HTMLUListElement
	SidebarTopicsID = "sidebar-topics"
	// TopicHeadingCountID identifies the selected topic's heading badge.
	//dom:id HTMLSpanElement
	TopicHeadingCountID = "topic-heading-count"
	// ChannelDOMPrefix preserves the sidebar entry's hexadecimal suffix.
	//dom:suffix HTMLLIElement
	ChannelDOMPrefix = "channel-"
	// TopicDOMPrefix preserves the sidebar entry's hexadecimal suffix.
	//dom:suffix HTMLLIElement
	TopicDOMPrefix = "topic-"
	// LinkChannelDOMPrefix identifies a channel link for focus restoration.
	//dom:suffix HTMLAnchorElement
	LinkChannelDOMPrefix = "link-channel-"
	// LinkTopicDOMPrefix identifies a topic link for focus restoration.
	//dom:suffix HTMLAnchorElement
	LinkTopicDOMPrefix = "link-topic-"
	// MemberAttribute carries the current membership identity.
	//dom:attribute
	MemberAttribute = "data-member"
	// SessionAttribute carries the signing-in session identity.
	//dom:attribute
	SessionAttribute = "data-session"
	// SessionExpiresAttribute carries the session expiry timestamp in milliseconds.
	//dom:attribute
	SessionExpiresAttribute = "data-session-expires"
	// RenderedAtAttribute carries the page render timestamp in milliseconds.
	//dom:attribute
	RenderedAtAttribute = "data-rendered-at"
	// SidebarTopicAttribute carries the selected topic identity.
	//dom:attribute
	SidebarTopicAttribute = "data-sidebar-topic"
	// SnapshotSeqAttribute carries the sidebar snapshot sequence.
	//dom:attribute
	SnapshotSeqAttribute = "data-snapshot-seq"
	// UnreadCountAttribute carries the server's unread projection.
	//dom:attribute
	UnreadCountAttribute = "data-unread-count"
	// SidebarResyncEvent requests the existing recovery form submission.
	//dom:event SidebarResyncDetail
	SidebarResyncEvent = "sidebar-resync"
)

// SidebarResyncDetail is empty because recovery reads the form's own state.
type SidebarResyncDetail struct{}

// Composer hooks also cover the stream and drafts names the composer shares.
const (
	// MessageBodyID identifies the message-body hook in server markup.
	//dom:id HTMLTextAreaElement
	MessageBodyID = "message-body"
	// MessageComposerID identifies the message-composer hook in server markup.
	//dom:id HTMLFormElement
	MessageComposerID = "message-composer"
	// MessageComposerStateID identifies the message-composer-state hook in server markup.
	//dom:id HTMLDivElement
	MessageComposerStateID = "message-composer-state"
	// MessageHelpID identifies the message-help hook in server markup.
	//dom:id HTMLParagraphElement
	MessageHelpID = "message-help"
	// MessageSendAlertID identifies the message-send-alert hook in server markup.
	//dom:id HTMLParagraphElement
	MessageSendAlertID = "message-send-alert"
	// MessageSendingID identifies the message-sending hook in server markup.
	//dom:id HTMLParagraphElement
	MessageSendingID = "message-sending"
	// MessageStatusID identifies the message-status hook in server markup.
	//dom:id HTMLDivElement
	MessageStatusID = "message-status"
	// MessagePendingID identifies the message-pending hook in server markup.
	//dom:id HTMLOListElement
	MessagePendingID = "message-pending"
	// MessagePendingTemplateID identifies the message-pending-template hook in server markup.
	//dom:id HTMLTemplateElement
	MessagePendingTemplateID = "message-pending-template"
	// MessagePendingBodyID identifies the message-pending-body hook in server markup.
	//dom:id HTMLParagraphElement
	MessagePendingBodyID = "message-pending-body"
	// MessagePendingEditHelpID identifies the message-pending-edit-help hook in server markup.
	//dom:id HTMLSpanElement
	MessagePendingEditHelpID = "message-pending-edit-help"
	// QuoteFormID identifies the quote-form hook in server markup.
	//dom:id HTMLFormElement
	QuoteFormID = "quote-form"
	// ComposerQuoteID identifies the composer-quote hook in server markup.
	//dom:id HTMLDivElement
	ComposerQuoteID = "composer-quote"
	// QuoteTextID identifies the quote-text hook in server markup.
	//dom:id HTMLTextAreaElement
	QuoteTextID = "quote-text"
	// TypingFormID identifies the typing-form hook in server markup.
	//dom:id HTMLFormElement
	TypingFormID = "typing-form"
	// TypingStatusID identifies the typing-status hook in server markup.
	//dom:id HTMLDivElement
	TypingStatusID = "typing-status"
	// MessageListID identifies the message-list hook in server markup.
	//dom:id HTMLDivElement
	MessageListID = "message-list"
	// MessageItemsID identifies the message-items hook in server markup.
	//dom:id HTMLOListElement
	MessageItemsID = "message-items"
	// MessageEmptyID identifies the message-empty hook in server markup.
	//dom:id HTMLDivElement
	MessageEmptyID = "message-empty"
	// MessagePendingEditHelpPrefix keeps cloned help ids unique by send key.
	//dom:suffix HTMLSpanElement
	MessagePendingEditHelpPrefix = "message-pending-edit-help-"
	// PostedMessageAttribute names the composer's posted-message contract.
	//dom:attribute
	PostedMessageAttribute = "data-posted-message"
	// PostedTopicAttribute names the composer's posted-topic contract.
	//dom:attribute
	PostedTopicAttribute = "data-posted-topic"
	// PostedSeqAttribute names the composer's posted-seq contract.
	//dom:attribute
	PostedSeqAttribute = "data-posted-seq"
	// SendKeyAttribute names the composer's send-key contract.
	//dom:attribute
	SendKeyAttribute = "data-send-key"
	// SentBodyAttribute names the composer's sent-body contract.
	//dom:attribute
	SentBodyAttribute = "data-sent-body"
	// InvalidAttribute names the composer's invalid contract.
	//dom:attribute
	InvalidAttribute = "data-invalid"
	// UnconfirmedKeyAttribute names the composer's unconfirmed-key contract.
	//dom:attribute
	UnconfirmedKeyAttribute = "data-unconfirmed-key"
	// PendingBodyAttribute names the composer's pending-body contract.
	//dom:attribute
	PendingBodyAttribute = "data-pending-body"
	// PendingFormAttribute names the composer's pending-form contract.
	//dom:attribute
	PendingFormAttribute = "data-pending-form"
	// PendingActionsAttribute names the composer's pending-actions contract.
	//dom:attribute
	PendingActionsAttribute = "data-pending-actions"
	// PendingEditAttribute names the composer's pending-edit contract.
	//dom:attribute
	PendingEditAttribute = "data-pending-edit"
	// PendingEditHelpAttribute names the composer's pending-edit-help contract.
	//dom:attribute
	PendingEditHelpAttribute = "data-pending-edit-help"
	// PendingRemoveAttribute names the composer's pending-remove contract.
	//dom:attribute
	PendingRemoveAttribute = "data-pending-remove"
	// PendingSendingAttribute names the composer's pending-sending contract.
	//dom:attribute
	PendingSendingAttribute = "data-pending-sending"
	// StateAttribute names the composer's state contract.
	//dom:attribute
	StateAttribute = "data-state"
	// DraftKeyAttribute names the composer's draft-key contract.
	//dom:attribute
	DraftKeyAttribute = "data-draft-key"
	// UnconfirmedAttribute names the composer's unconfirmed contract.
	//dom:attribute
	UnconfirmedAttribute = "data-unconfirmed"
	// NotSentAttribute names the composer's not-sent contract.
	//dom:attribute
	NotSentAttribute = "data-not-sent"
	// StillUnconfirmedAttribute names the composer's still-unconfirmed contract.
	//dom:attribute
	StillUnconfirmedAttribute = "data-still-unconfirmed"
	// RecoveredAttribute names the composer's recovered contract.
	//dom:attribute
	RecoveredAttribute = "data-recovered"
	// RepeatAttribute names the composer's repeat contract.
	//dom:attribute
	RepeatAttribute = "data-repeat"
	// BusyAttribute names the composer's busy contract.
	//dom:attribute
	BusyAttribute = "data-busy"
	// CapacityAttribute names the composer's capacity contract.
	//dom:attribute
	CapacityAttribute = "data-capacity"
	// DraftsHorizonAttribute carries the sign-out horizon on the sign-in form.
	//dom:attribute
	DraftsHorizonAttribute = "data-drafts-horizon"
	// DraftChannelAttribute names the composer's draft-channel contract.
	//dom:attribute
	DraftChannelAttribute = "data-draft-channel"
	// DraftTopicAttribute names the composer's draft-topic contract.
	//dom:attribute
	DraftTopicAttribute = "data-draft-topic"
	// DraftMarkerAttribute marks sidebar draft indicators.
	//dom:attribute
	DraftMarkerAttribute = "data-draft-marker"
	// EventCursorAttribute names the composer's event-cursor contract.
	//dom:attribute
	EventCursorAttribute = "data-event-cursor"
	// AppliedCursorAttribute names the composer's applied-cursor contract.
	//dom:attribute
	AppliedCursorAttribute = "data-applied-cursor"
	// TopicAttribute names the composer's topic contract.
	//dom:attribute
	TopicAttribute = "data-topic"
	// FromTopicAttribute names the composer's from-topic contract.
	//dom:attribute
	FromTopicAttribute = "data-from-topic"
	// MessageSendingEvent carries message-sending notifications.
	// The markup id and script event deliberately share this name; literal checks must allow both.
	//dom:event MessageSendingDetail
	MessageSendingEvent = "message-sending"
	// MessageSentEvent carries message-sent notifications.
	//dom:event MessageSentDetail
	MessageSentEvent = "message-sent"
	// MessagePendingActionEvent carries message-pending-action notifications.
	//dom:event MessagePendingActionDetail
	MessagePendingActionEvent = "message-pending-action"
	// MessageDraftRestoredEvent carries message-draft-restored notifications.
	//dom:event MessageDraftRestoredDetail
	MessageDraftRestoredEvent = "message-draft-restored"
	// PendingSendEvent triggers the existing htmx form.
	//dom:event PendingSendDetail
	PendingSendEvent = "pending-send"
	// TypingInputEvent triggers the existing htmx form.
	//dom:event TypingInputDetail
	TypingInputEvent = "typing-input"
)

// MessageSendingDetail binds a draft snapshot to its sending identity.
// Key is null in the contract when the page has no idempotency key; until #1824,
// the unchanged legacy composer sends undefined, which no consumer distinguishes
// from null (see docs/architecture/web-contracts-generated.md).
type MessageSendingDetail struct {
	Key     *string `json:"key"`
	Body    string  `json:"body"`
	InPlace bool    `json:"inPlace"`
}

// MessageSentDetail correlates a draft with its confirmed message DOM id.
// Key is null when the page has no idempotency key.
type MessageSentDetail struct {
	Key     *string `json:"key"`
	Message string  `json:"message"`
}

// MessagePendingActionDetail distinguishes restoration from deliberate removal.
type MessagePendingActionDetail struct {
	Key         string  `json:"key"`
	RestoredKey *string `json:"restoredKey"`
	Kind        string  `json:"kind" dom:"edit,remove"`
}

// MessageDraftRestoredDetail preserves the identity restored by the drafts script.
type MessageDraftRestoredDetail struct {
	Key string `json:"key"`
}

// PendingSendDetail is empty because htmx reads the pending form.
type PendingSendDetail struct{}

// TypingInputDetail is empty because htmx reads the typing form.
type TypingInputDetail struct{}
