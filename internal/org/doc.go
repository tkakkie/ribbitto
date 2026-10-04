// Package org is the org module's root (decision 26) and the single place
// that decides who may see an organisation's data. Handlers and the
// real-time delivery loop call it; they never decide access themselves.
//
// Feature: org (feature map in docs/architecture/features.md), which owns the
// organization, member and setup tables. Exported API: Organization, Member,
// Role, ValidateOrganizationName, ValidateSlug, ValidateHandle; Authorizer,
// built by NewAuthorizer (including MayReceive, which the real-time stream
// calls for each event after rendering it, right before sending), Membership,
// the MembershipStore interface it needs and ErrNotFound. Every feature checks
// access through it.
//
// HandleChanger (NewHandleChanger, with the MembershipResolver and
// HandleStore it needs, ErrInvalidHandle and ErrHandleTaken) changes the
// caller's own handle: the member it changes always comes from the
// Authorizer (the session and the URL's organisation), never from a request.
// Directory looks members up for author names, one organisation at a time,
// returning DirectoryEntry values. KindJoined with Joined, EncodeJoined,
// DecodeJoined and RouteJoined is the member.joined kind org publishes, its
// payload and its routing. AccountCreator and AccountCreatorIn declare the
// account write setup and sign-up will inject. TxRunner, RegistrationWriter
// with RegistrationWriterIn, SetupState and ErrSlugUnavailable are the
// transaction runner, org's transaction-bound writes and the setup-state read
// through which setup and sign-up will own their transaction (3.12, 3.11).
//
// Setup (NewSetup, SetupInput, SetupResult, SetupStore, ErrSetupToken and
// ErrSetupCompleted) authorizes and validates installation-wide first-run setup
// before hashing. SignUp (NewSignUp, SignUpStore, ErrSignUpClosed and
// ErrEmailTaken) controls installation-wide registration. Both share
// ValidationErrors and the account-field validation; ErrHandleTaken is shared
// with HandleChanger.
// Listed exceptions: setup creates identity's first account and the default
// channel in the organisation transaction; sign-up creates identity's account
// with its member and advances organization.event_seq in the same transaction.
//
// Org's store implements MembershipStore, HandleStore, the snapshot-bound
// Directory, RegistrationWriter and SetupState. orgpg builds the authorizer
// and handle changer, binds the directory to the caller's snapshot
// (MembersIn) and the registration writes to the caller's transaction, and
// implements TxRunner over the pool. infra/postgres implements
// SetupStore and SignUpStore, keeping their transactions until 3.12 and 3.11.
package org
