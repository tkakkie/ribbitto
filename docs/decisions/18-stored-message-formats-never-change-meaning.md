# 18. Stored message formats never change meaning

**Decided:** M2 message bodies are plain text, with no `format` column.
Adding Markdown or rich text later must also add an explicit format (for
example `plain_text`, `markdown`, `rich_text_v1`) and mark every existing
message `plain_text`. A stored message is never reinterpreted in a new
format. The stored body is the source of truth; rendered HTML may only be
a derived, disposable cache. The channel list implements this by escaping
stored bodies through templ, preserving line breaks and isolating direction.
**Why:** new rendering features must not change what earlier authors wrote
or turn their literal text into markup (#74).
**Considered:** inferring a format or reinterpreting all history when a
renderer changes (breaks compatibility); storing rendered HTML as the
source (loses the original text).
