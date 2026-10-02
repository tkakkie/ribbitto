# 12. UI screenshots only when the look is the point

**Decided:** a pull request that changes the UI says how to see the change
(`make dev`, the URL, any configuration) and what was checked in a browser.
Screenshots are needed only when the look itself is the point — design
tokens, layout, visual polish — or when the maintainer asks.
**Why:** the AI session cannot upload images to GitHub, and for most UI
changes the checks that matter (CSP, texts in both languages, secrets never
echoed, status codes) are tests and a browser run, not a picture (#90).
**Considered:** a screenshot for every UI change (the earlier rule; it cost
a maintainer round trip on #56 and was not followed afterwards);
screenshots taken by CI as workflow artifacts (no upload problem, but more
tooling; revisit when M5 makes screenshots frequent).
