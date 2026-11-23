# torrent-store

Torrent store service with multiple backends and GRPC-access.

## Server usage

```
./torrent-store help serve 
NAME:
   torrent-store serve - Serves web server

USAGE:
   torrent-store serve [command options] [arguments...]

OPTIONS:
   --probe-host value                  probe listening host [$PROBE_HOST]
   --probe-port value                  probe listening port (default: 8081) [$PROBE_PORT]
   --use-probe                         enable probe [$USE_PROBE]
   --aws-access-key-id value           AWS Access Key ID [$AWS_ACCESS_KEY_ID]
   --aws-secret-access-key value       AWS Secret Access Key [$AWS_SECRET_ACCESS_KEY]
   --aws-endpoint value                AWS Endpoint [$AWS_ENDPOINT]
   --aws-region value                  AWS Region [$AWS_REGION]
   --aws-no-ssl                         [$AWS_NO_SSL]
   --redis-host value                  redis host (default: "localhost") [$REDIS_MASTER_SERVICE_HOST, $ REDIS_SERVICE_HOST]
   --redis-port value                  redis port (default: 6379) [$REDIS_MASTER_SERVICE_PORT, $ REDIS_SERVICE_PORT]
   --redis-pass value                  redis pass [$REDIS_PASS]
   --redis-user value                  redis user (default: "default") [$REDIS_USER]
   --redis-sentinel-port value         redis sentinel port (default: 0) [$REDIS_SERVICE_PORT_REDIS_SENTINEL]
   --redis-sentinel-master-name value  redis sentinel master name (default: "mymaster") [$REDIS_SERVICE_SENTINEL_MASTER_NAME]
   --pprof-host value                  pprof listening host [$PPROF_HOST]
   --pprof-port value                  pprof listening port (default: 8082) [$PPROF_PORT]
   --use-pprof                         enable pprof [$USE_PPROF]
   --grpc-host value                   grpc listening host [$GRPC_HOST]
   --grpc-port value                   grpc listening port (default: 50051) [$GRPC_PORT]
   --badger-expire value               badger expire (sec) (default: 3600) [$BADGER_EXPIRE]
   --redis-expire value                redis expire (sec) (default: 86400) [$REDIS_EXPIRE]
   --use-redis                         use redis [$USE_REDIS]
   --aws-bucket value                  s3 store bucket (default: "torrent-store") [$AWS_BUCKET]
   --use-s3                            use s3 [$USE_S3]
   --abuse-host value                  abuse store host [$ABUSE_STORE_SERVICE_HOST]
   --abuse-port value                  port of the redis service (default: 50051) [$ABUSE_STORE_SERVICE_PORT]
   --use-abuse                         use abuse [$USE_ABUSE]
   --stoplist-path value               stoplist path [$STOPLIST_PATH]
```

## Private torrents

A torrent with the BEP 27 `private` flag is stored info-only. Every Push drops
its announce, announce-list and url-list, and a Push that finds an older copy
still carrying lists rewrites it without them. A Push with `Caller.uid` keeps
that user's trackers in a sealed record (`private/<uid>/<hash>` in S3; needs
`PRIVATE_TRACKERS_KEY` and S3).

Pull, Files and Touch of a private torrent decide by the caller:

- `Caller.internal`: allowed; Pull returns the info-only copy;
- `Caller.uid` with a record: allowed; Pull returns the copy with that record's
  trackers and `caller_trackers` set;
- `Caller.uid` without a record: refused, `no_record`;
- empty `Caller`, or none at all: refused, `sign_in`.

This is the only mode. The `PRIVATE_ACCESS_ENFORCE` switch of earlier releases
is gone, and the variable is ignored if still set.

## Client usage

It is connecting to local server instance localhost:50051. It sends no
`Caller`: a private torrent it pushes is stored without trackers, and pulling
or touching one is refused.

```
% ./client help
NAME:
   torrent-store-client - interacts with torrent store

USAGE:
   client [global options] command [command options] [arguments...]

VERSION:
   0.0.1

COMMANDS:
   touch, to  touches torrent
   push, ps   pushes torrent to the store
   pull, pl   pulls torrent from the store
   help, h    Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --host value, -H value  hostname of the torrent store (default: "localhost") [$TORRENT_STORE_HOST]
   --port value, -P value  port of the torrent store (default: 50051) [$TORRENT_STORE_PORT]
   --help, -h              show help
   --version, -v           print the version
```