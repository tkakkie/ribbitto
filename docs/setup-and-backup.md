# First-run setup and backup restore

Creating the initial organisation and owner on a new [ribbitto](../README.md)
instance, opening sign-up, and restoring a database backup; see the
[documentation index](README.md) for the other guides.

## First-run setup

After running migrations, set `RIBBITTO_SETUP_TOKEN` to at least 32 random
characters before starting the server, then open `/setup`. Enter that token,
the organisation name and URL slug, and the owner's display name, email and
password. Successful setup signs the owner in and redirects to `/`.
An unset or empty token disables setup; a non-empty token shorter than 32
characters prevents startup. After setup, both GET and POST `/setup` return
404 even if the token remains in the environment.

Set `RIBBITTO_SIGNUP=on` to let people register at `/signup` after setup.
They join the setup organisation as members and are signed in automatically.
`off`, empty or unset disables sign-up (GET and POST return 404); any other
value prevents startup. Sign-in links to registration only while it is open.

## Restoring a backup

Stop ribbitto, restore the database, then start ribbitto. The process keeps
sequence state in memory (the real-time hub and its caches), so rolling the
database back under a running process is not supported. After the restart, a
browser that reconnects with a cursor above the restored log gets `reset` and
reloads the page
([replay](architecture/replay.md#ordering-and-replay)).
