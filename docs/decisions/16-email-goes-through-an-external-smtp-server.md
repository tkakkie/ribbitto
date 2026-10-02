# 16. Email goes through an external SMTP server

**Decided:** ribbitto sends mail through an external SMTP server and runs
no mail server of its own. It speaks standard SMTP, not a vendor API; SES,
Postfix, SendGrid, Mailgun and others are used through SMTP. ribbitto
starts and works without SMTP configured. Enabling a feature that needs
delivery (password reset, email verification, invitation mail) makes the
SMTP settings required, and startup fails without them. A non-sending
Mailer serves development and tests. Until delivery exists, the identity
rules in `docs/domain/invariants.md` (invariant 8) stand: email addresses are not
verified and prove nothing. The plan and its order are in #128.
**Why:** running a mail server well (deliverability, reputation, abuse
handling) is a product of its own; every self-hoster already has, or can
rent, an SMTP relay. Keeping SMTP optional lets a small instance run with
none, while a feature that cannot work without mail fails loudly instead of
silently.
**Considered:** a built-in mail server (large and hard to operate
safely); a vendor API such as SES or SendGrid (ties self-hosters to one
provider); making SMTP always required (blocks the simplest installs).
