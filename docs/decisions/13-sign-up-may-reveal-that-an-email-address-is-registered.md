# 13. Sign-up may reveal that an email address is registered

**Decided:** while `RIBBITTO_SIGNUP=on`, public sign-up answers a duplicate
email with "already registered". Anyone can therefore confirm that an
address has an account. The sign-up rate limits only slow that probing
(3 attempts, then one every 10 minutes per client); they do not prevent it.
Sign-in gives unknown emails and wrong passwords the same response, with a
dummy hash to reduce timing differences, and setup has no account-existence
response.
**Why:** M1 has unique email addresses, signs a new account in at once, and
sends no email. Under those requirements there is no honest response that
is the same for new and existing addresses (#39; audit #93, F2).
**Considered:** answering every sign-up with "check your inbox" and telling
an existing owner by email, which needs email delivery (revisit with
invitations or email verification); allowing duplicate unverified
addresses (conflicts with unique email as the sign-in identifier).
