package domain

// Role is a member's role in an organisation.
type Role string

// The two roles of the MVP.
const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
)

// Organization is a workspace; it owns everything its members create.
type Organization struct {
	ID   ID
	Slug string
	Name string
}

// Member is an account's membership in one organisation. Organisation-owned
// data refers to members, never directly to accounts.
type Member struct {
	ID             ID
	OrganizationID ID
	AccountID      ID
	Role           Role
	// Handle helps people tell members apart within the organisation. It
	// can change, so nothing stores or authorises by it: ID does that.
	Handle string
}
