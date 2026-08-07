protoc:
	protoc proto/torrent-store.proto --go_out=. --go_opt=paths=source_relative \
		   --go-grpc_out=. --go-grpc_opt=paths=source_relative proto/torrent-store.proto

# services links both our proto and the abuse-store client's, and the two
# register the same message names — the registry panics at init, so a bare
# `go test ./...` never reaches a single test. Same flag, same reason as in
# web-ui's Makefile.
PROTO_CONFLICT_LDFLAGS := -X google.golang.org/protobuf/reflect/protoregistry.conflictPolicy=ignore

test:
	go test -race -ldflags '$(PROTO_CONFLICT_LDFLAGS)' ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

.PHONY: protoc test vet fmt
