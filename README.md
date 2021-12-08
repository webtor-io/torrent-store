# torrent-store

Torrent store service with multiple backends and GRPC-access.

## Server usage

```
$ ./torrent-store help serve
NAME:
   torrent-store serve - Serves web server

USAGE:
   torrent-store serve [command options] [arguments...]

OPTIONS:
   --probe-host value                  probe listening host
   --probe-port value                  probe listening port (default: 8081)
   --aws-access-key-id value           AWS Access Key ID [$AWS_ACCESS_KEY_ID]
   --aws-secret-access-key value       AWS Secret Access Key [$AWS_SECRET_ACCESS_KEY]
   --aws-endpoint value                AWS Endpoint [$AWS_ENDPOINT]
   --aws-region value                  AWS Region [$AWS_REGION]
   --aws-no-ssl                         [$AWS_NO_SSL]
   --redis-host value                  redis host (default: "localhost") [$REDIS_MASTER_SERVICE_HOST, $ REDIS_SERVICE_HOST]
   --redis-port value                  redis port (default: 6379) [$REDIS_MASTER_SERVICE_PORT, $ REDIS_SERVICE_PORT]
   --redis-pass value                  redis pass [$REDIS_PASS]
   --redis-sentinel-port value         redis sentinel port (default: 0) [$REDIS_SERVICE_PORT_REDIS_SENTINEL]
   --redis-sentinel-master-name value  redis sentinel master name (default: "mymaster") [$REDIS_SERVICE_SENTINEL_MASTER_NAME]
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
```

## Client usage

It is connecting to local server instance localhost:50051.

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