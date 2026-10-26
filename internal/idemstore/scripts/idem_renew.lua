-- Renew the K7 lease for the request that still owns it.
--
-- KEYS [1] idem key  [2] idem lock key
-- ARGV [1] request_id  [2] ttl_ms
--
-- K6 carries the 24h idempotency cache and must NOT be shortened to the 15s
-- lease: the durable row already exists, but dropping the cache would send
-- every replay to the primary database for the rest of the request's life.
-- The lease that needs heartbeats is K7 (and the DB row, renewed separately).

if redis.call("GET", KEYS[2]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[2], ARGV[2])
end
return 0
