// sqliteab is a NESTED module (like ../qosab/netemab and ../../../third_party/yamux)
// so mattn/go-sqlite3's cgo SQLite amalgamation and psanford/sqlite3vfs never
// enter cornus's main go.mod/go.sum and the normal pure-Go `go test ./...` gate
// stays fast. It imports cornus/pkg/wire (the block protocol under test) via the
// local replace, and always builds the in-repo yamux fork via its replace. Run
// explicitly:
//
//	cd pkg/wire/sqliteab && go test -run TestSQLite -v
//	cd pkg/wire/sqliteab && go test -run '^$' -bench . -benchmem
module cornus/pkg/wire/sqliteab

go 1.26.0

require (
	cornus v0.0.0
	github.com/hashicorp/yamux v0.1.2
	github.com/hugelgupf/p9 v0.4.1
	github.com/mattn/go-sqlite3 v1.14.28
	github.com/psanford/sqlite3vfs v0.0.0-20260519004904-f9180fa2acc9
)

require (
	github.com/coder/websocket v1.8.15 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/moby/patternmatcher v0.6.0 // indirect
	github.com/u-root/uio v0.0.0-20240224005618-d2acac8f3701 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
)

replace cornus => ../../..

replace github.com/hashicorp/yamux => ../../../third_party/yamux
