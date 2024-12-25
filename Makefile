protoc:
	protoc proto/torrent-store.proto --go_out=. --go_opt=paths=source_relative \
		   --go-grpc_out=. --go-grpc_opt=paths=source_relative proto/torrent-store.proto