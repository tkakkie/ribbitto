# Web toolchain checks (planned)

Checks and measurement requirements for the [web toolchain](web-toolchain.md).

## Checks and measurement — #1412, #1414, #1426

Adapt `web/static/javascript_test.go` to inspect authored TypeScript and
remaining JavaScript, allow module-scoped declarations without global writes,
and recognize the converted drafts module's storage ownership. Add TypeScript
syntax fixtures and hashed-manifest checks while preserving forbidden APIs,
requests, storage and HTML-manipulation intent. Regex is no parser or security
boundary; types do not cure alias,
computed-property or multiline blind spots. Keep browser tests.

Each issue's pre-`ready` measurement record contains, for all five metrics in
decision 43: baseline SHA, page/path and scenario, device/browser and profile,
method, sample count and aggregation, and numeric allowed head-minus-base
tolerance with units. Include vendor and synchronous scripts in whole-page
totals; report raw bytes too. Use separate response gzip streams (`gzip -9` on
stdin) and state whether request counts are graph counts or browser
observations. Measurement approval, cap, alternating base/head and
quiet-machine rules still follow
[investigations](../workflow/investigations.md) and decision 40 §5. A claimed
unaffected metric needs an explicit method and numeric tolerance, not an
unstated exemption. #1426 sets milestone limits before `ready` too.

