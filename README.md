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

A torrent with the BEP 27 `private` flag is stored info-only: a Push drops its
announce, announce-list and url-list before it writes, and a Push that finds an
older S3 copy still carrying lists rewrites that copy without them. A Push
heals only the durable S3 copy; a list-carrying Redis or Badger copy above an
info-only S3 copy stays until it expires or `private-rewrite` and a restart
clear it (below).

A private Push with a `Caller` keeps that user's trackers in a sealed record
(`private/<uid>/<hash>` in S3), which needs `PRIVATE_TRACKERS_KEY` and S3.
Without them the Push is refused with `private_unavailable`, and a `Caller`
without a uid gets `private_needs_account`; nothing is stored then.

Pull, Files and Touch of a private torrent decide by the caller:

- no `Caller` at all: refused, `sign_in`;
- `Caller.internal`: allowed; Pull returns the info-only copy;
- records off (no key or no S3): every other caller is refused,
  `private_unavailable`;
- `Caller` without a uid: refused, `sign_in`;
- `Caller.uid` with a record: allowed; Pull returns the copy with that record's
  trackers and `caller_trackers` set;
- `Caller.uid` without a record: refused, `no_record`.

This is the only mode. The `PRIVATE_ACCESS_ENFORCE` switch of earlier releases
is gone, and the variable is ignored if still set.

## Stripping trackers from stored private torrents

A private torrent's announce, announce-list and url-list carry the uploader's
passkey. A private Push stores info-only metainfo (above), but copies stored
before phase 4 still hold the first uploader's lists. `private-rewrite` rewrites each
such S3 object in place as info-only (same key, same infohash) and deletes its
Redis copy, so the next Pull backfills the clean one.

- Dry run unless `--write`: without it nothing is written anywhere.
- Only the bucket root is listed (delimiter `/`), so `<hash>` objects are read
  and private records under `private/` are never listed or read. No
  `PRIVATE_TRACKERS_KEY` is needed.
- stdout: one JSON object of counts at the end, also on Ctrl-C or SIGTERM.
  stderr: a progress line every `--progress-every` torrents and one
  `key<TAB>op<TAB>class` line per failure. Neither ever carries a body, a URL
  or a host.

Run it in a serving pod, which already has the S3 and Redis environment, so
nobody handles credentials:

```
# 1. size of the bucket: counts keys, reads nothing
kubectl -n webtor exec deploy/torrent-store -- env GOMEMLIMIT=256MiB /server private-rewrite --list-only
# 2. dry run: what --write would change
kubectl -n webtor exec -it deploy/torrent-store -- env GOMEMLIMIT=256MiB /server private-rewrite
# 3. ONLY after the owner's go-ahead
kubectl -n webtor exec -it deploy/torrent-store -- env GOMEMLIMIT=256MiB /server private-rewrite --write
# 4. Badger is per pod and out of the tool's reach: restart to flush it
kubectl -n webtor rollout restart deploy/torrent-store
# 5. verify: private_with_lists and redis_with_lists are 0 (hash_mismatch objects excepted)
kubectl -n webtor exec -it deploy/torrent-store -- env GOMEMLIMIT=256MiB /server private-rewrite
```

- The tool shares the pod's memory limit with the server: an OOM kills the
  server too. Keep `GOMEMLIMIT` and the default `--concurrency 4`.
- `--limit N` bounds a run; `--start-after <last_key>` resumes one. `last_key`
  only moves past finished listing pages, so resuming re-reads at most one
  page. Ctrl-C reaches the process only through `kubectl exec -it`. Without a
  TTY, Ctrl-C stops only the local kubectl while the run goes on in the pod,
  and `pkill server` would also kill the serving process (same binary name):
  run `--write` with `-it`, and otherwise take `last_key` from the last
  progress line.
- Run `--write` only on an image where every write of a private hash is
  info-only, including the merge onto an old stored copy (the phase-4
  enforce-only change, which clears the stored copy's lists on any private
  Push). On an older image, a Push of an old private hash whose copy still
  holds a default tracker re-writes that copy with its lists; step 5 shows it.
  If step 5 is not 0, run `--write` again; objects counted as `hash_mismatch`
  are never rewritten and stay in `private_with_lists`.
- **Old copies survive outside the live objects.** If the JSON reports
  `"versioning": "Enabled"` or `"Suspended"`, earlier object versions still
  hold the lists, and so does any S3 backup or replica. The rewrite does not
  touch them.

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