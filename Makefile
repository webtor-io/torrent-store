protoc:
	protoc -I proto/ proto/torrent-store.proto --go_out=plugins=grpc:proto