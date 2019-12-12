# torrent-store

Temporary torrent storage with Redis backend and GRPC-access.
Here is two parts: server and client.

## Server usage

```
% ./server help
NAME:
   torrent-store-server - runs torrent store

USAGE:
   server [global options] command [command options] [arguments...]

VERSION:
   0.0.1

COMMANDS:
   help, h  Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --redis-host value, --rH value         hostname of the redis service [$REDIS_MASTER_SERVICE_HOST, $ REDIS_SERVICE_HOST]
   --redis-port value, --rP value         port of the redis service (default: 6379) [$REDIS_MASTER_SERVICE_PORT, $ REDIS_SERVICE_PORT]
   --abuse-store-host value, --asH value  hostname of the abuse store [$ABUSE_STORE_SERVICE_HOST]
   --abuse-store-port value, --asP value  port of the redis service (default: 50051) [$ABUSE_STORE_SERVICE_PORT]
   --redis-db value, --rDB value          redis db (default: 0) [$REDIS_DB]
   --redis-password value, --rPASS value  redis password [$REDIS_PASS, $ REDIS_PASSWORD]
   --host value, -H value                 listening host
   --port value, -P value                 listening port (default: 50051)
   --help, -h                             show help
   --version, -v                          print the version
```

## Client usage

It is connecting to local server instance localhost:50051.

```
% ./client
NAME:
   torrent-store-client-cli - interacts with torrent store

USAGE:
   client [global options] command [command options] [arguments...]

VERSION:
   0.0.1

COMMANDS:
   push, ps  pushes torrent to the store
   pull, pl  pulls torrent from the store
   help, h   Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --host value, -H value  hostname of the torrent store (default: "localhost") [$TORRENT_STORE_HOST]
   --port value, -P value  port of the torrent store (default: 50051) [$TORRENT_STORE_PORT]
   --help, -h              show help
   --version, -v           print the version
```