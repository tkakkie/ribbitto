# Vendored assets

JavaScript files are copied unchanged from the pinned npm tarballs below.
htmx loads before its SSE and idiomorph extensions.
No npm installation or JavaScript build step is needed.

When updating a vendored JavaScript file, update its pinned SHA-256 in
[`vendor_test.go`](../vendor_test.go) and this README in the same pull
request. The test covers only `vendor/*.js`. The sprite's SHA-256 below
is the source tarball's hash, checked by `tools/buildicons`.

## `htmx-2.0.7.min.js`

- Version: `2.0.7`
- Source: https://registry.npmjs.org/htmx.org/-/htmx.org-2.0.7.tgz (`package/dist/htmx.min.js`)
- SHA-256: `60231ae6ba9db3825eb15a261122d5f55921c4d53b66bf637dc18b4ee27c79f9`

## `htmx-ext-sse-2.2.4.min.js`

- Version: `2.2.4`
- Source: https://registry.npmjs.org/htmx-ext-sse/-/htmx-ext-sse-2.2.4.tgz (`package/dist/sse.min.js`)
- SHA-256: `98a46496de0c3605fbffdce9167ba427bdd9553184f83f149c261891a92c0136`

## `idiomorph-ext-0.7.4.min.js`

- Version: `0.7.4`
- Source: https://registry.npmjs.org/idiomorph/-/idiomorph-0.7.4.tgz (`package/dist/idiomorph-ext.min.js`)
- SHA-256: `a6437e55b1b6a07bc421f0d230266a39399b6826c6ed19e0ed9c63b707444a5f`

## `fluent-icons-1.1.344.svg`

Fluent UI System Icons, MIT, from `@fluentui/svg-icons`:

- Version: `1.1.344`
- Source: https://registry.npmjs.org/@fluentui/svg-icons/-/svg-icons-1.1.344.tgz
- SHA-256: `6fe61f489a6d0d065ec2182794fdadf0e15db7d3388997c5ba8039dcfaaa35e8`
- Licence: `fluent-icons-LICENSE`. npm omits it; copied unchanged from
  https://raw.githubusercontent.com/microsoft/fluentui-system-icons/f1260014e8a4fcfa6777182fd6cf082fc5e952d8/LICENSE
  (the pinned package metadata's `gitHead`).
- Licence SHA-256: `69bc45dc42b9acb96a69823adbc6ae538374e3c0bde169b855b32c48eaaef52f`

Each row includes **Regular and Filled, 20 px**. The source path is
`package/icons/<Fluent name>_20_<style>.svg`; the symbol ID adds
`-regular` or `-filled` to the name below.

| Symbol name | Fluent name |
|---|---|
| home | home |
| add | add |
| search | search |
| settings | settings |
| people | people |
| panel-right | panel_right |
| info | info |
| chevron-down | chevron_down |
| chevron-right | chevron_right |
| checkmark | checkmark |
| send | send |
| arrow-up | arrow_up |
| branch | branch |
| dismiss | dismiss |
| pin | pin |
| sign-out | sign_out |

Rebuild from the repository root with `go -C tools run ./buildicons ..`
([`tools/buildicons`](../../../tools/buildicons/main.go), Go standard library
only). The development-only command fetches
both pinned sources, checks their SHA-256 hashes, and copies each SVG's
children unchanged into a named symbol with its original viewBox and
`fill="currentColor"`. It writes the sprite and licence beside this file;
no npm installation or JavaScript build step runs. Change the versioned
sprite filename and the component URL whenever the sprite changes, since
static assets are cached as immutable.
