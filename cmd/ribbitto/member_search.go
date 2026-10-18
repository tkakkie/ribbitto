package main

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
)

func newMemberSearch(pool *pgxpool.Pool) *org.MemberSearch {
	return orgpg.NewMemberSearch(pool, identitypg.AccountsIn)
}
