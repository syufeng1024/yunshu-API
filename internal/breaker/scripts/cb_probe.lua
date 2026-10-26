-- Promote OPEN -> HALF_OPEN when the backoff elapsed, then take the K17
-- single-flight probe lease.
--
-- KEYS [1] up:cb:{channel_id}  [2] up:probe:{channel_id}
-- ARGV [1] now_ms  [2] probe_ttl_ms  [3] request_id
--
-- Returns {acquired, state, open_until}. The probe lease is released with an
-- explicit DEL after the attempt (T06 hard constraint 15): relying on the 30s
-- TTL would make three successful probes take up to 90s to close the breaker.

local key = KEYS[1]
local probe = KEYS[2]
local now_ms = tonumber(ARGV[1])
local ttl_ms = tonumber(ARGV[2])
local request_id = ARGV[3]

local st = redis.call("HGET", key, "st") or "CLOSED"
local open_until = tonumber(redis.call("HGET", key, "open_until") or "0")

if st == "OPEN" then
  if now_ms < open_until then
    return {0, st, open_until}
  end
  redis.call("HSET", key, "st", "HALF_OPEN", "ps", 0)
  st = "HALF_OPEN"
end

if st ~= "HALF_OPEN" then
  return {0, st, 0}
end

local acquired = redis.call("SET", probe, request_id, "PX", ttl_ms, "NX")
if acquired == false then
  return {0, st, 0}
end
return {1, st, 0}
