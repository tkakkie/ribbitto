FROM golang@sha256:6928d94d1142af58816fed5a62338d605a266ccd53a0deaab6b1096f19059261

# No parser directive: the recipe must not fetch an unpinned frontend.
# Only arm64 has been exercised; tools/download does not pin this asset.
RUN set -eu; \
    test "$(dpkg --print-architecture)" = arm64; \
    curl -fsSL -o /usr/local/bin/tailwindcss https://github.com/tailwindlabs/tailwindcss/releases/download/v4.3.3/tailwindcss-linux-arm64; \
    echo "55fd0b241214eff3de1e8ee4f22796662f2d2e7a49bcfca7477cfd0bac398195  /usr/local/bin/tailwindcss" | sha256sum -c -; \
    chmod 0755 /usr/local/bin/tailwindcss

COPY tools/go.mod tools/go.sum /build/tools/
# sqlc generation is very slow without cgo; caches are unnecessary at runtime.
RUN set -eu; \
    cd /build/tools; \
    GOFLAGS=-mod=readonly CGO_ENABLED=1 go build -o /usr/local/bin/templ github.com/a-h/templ/cmd/templ; \
    GOFLAGS=-mod=readonly CGO_ENABLED=1 go build -o /usr/local/bin/sqlc github.com/sqlc-dev/sqlc/cmd/sqlc; \
    rm -rf /build /go/pkg/mod /root/.cache
