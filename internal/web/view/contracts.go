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
