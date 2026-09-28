// Package member holds use cases on a member's own membership, such as
// changing their handle.
//
// Feature: org (feature map in docs/architecture/README.md), which owns the
// member table. Exported API: Service, the Store and Authorizer interfaces
// it needs, and ErrInvalidHandle and ErrHandleTaken. The member it changes
// always comes from authz (the session and the URL's organisation), never
// from a request.
package member
