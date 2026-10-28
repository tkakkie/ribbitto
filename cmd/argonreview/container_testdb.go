package main

const (
	containerTestDatabaseURL = "RIBBITTO_TEST_DATABASE_URL=postgres://postgres:ribbitto-ci-only@127.0.0.1:5432/postgres?sslmode=disable"
	containerRequireDB       = "RIBBITTO_REQUIRE_DB=1"
	// Sized for max_connections=100 (#636); pinned against Makefile in tests.
	containerTestDBParallel    = "-p 2"
	containerTestDBStartFailed = 27
	containerTestDBScript      = containerTestDBPrelude + `go test -count=1 -json ` + containerTestDBParallel + ` "$1" || status=$?
` + containerTestDBFinish
	containerTestDBPrelude = containerCopyScript + containerTemplCommand + " >&2\n" + containerSQLCCommand + " >&2\n" + containerCSSCommand + " >&2\n" + `/usr/lib/postgresql/18/bin/initdb -D /home/check/pgdata -U postgres -E UTF8 --locale=C.UTF-8 --auth=trust --no-sync >&2
/usr/lib/postgresql/18/bin/postgres -D /home/check/pgdata -c listen_addresses=127.0.0.1 -c unix_socket_directories=/tmp -c fsync=off -c synchronous_commit=off -c full_page_writes=off -c max_connections=100 >&2 &
pgpid=$!
stop_postgres() {
 kill -TERM "$pgpid" 2>/dev/null || :
 wait "$pgpid" || :
}
trap stop_postgres 0
ready=0
tries=0
while [ "$tries" -lt 100 ]; do
 if ! kill -0 "$pgpid" 2>/dev/null; then break; fi
 if /usr/lib/postgresql/18/bin/pg_isready -h 127.0.0.1 -p 5432 -U postgres >&2; then
  ready=1
  break
 fi
 tries=$((tries + 1))
 sleep 0.1
done
if [ "$ready" != 1 ]; then
 stop_postgres
 trap - 0
 exit 27
fi
status=0
`
	containerTestDBFinish = `stop_postgres
trap - 0
exit "$status"
`
)
