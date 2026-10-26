-- Renew a lock only when the caller still owns it.
--
-- KEYS [1] lock key   ARGV [1] owner  [2] ttl_ms
--
-- PEXPIRE on a lock acquired by someone else would extend the wrong owner's
-- lease; the compare is therefore mandatory, not an optimization.

if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
