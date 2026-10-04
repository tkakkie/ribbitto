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
// account write setup and sign-up will inject.
//
// Org's store implements MembershipStore, HandleStore and the snapshot-bound
// Directory. orgpg builds the authorizer and handle changer and binds the
// directory to the caller's snapshot (MembersIn).
package org
