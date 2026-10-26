-- K16 circuit-breaker state machine.
--
-- KEYS [1] up:cb:{channel_id}
-- ARGV [1] now_ms      [2] fail_threshold  [3] base_open_ms
--      [4] max_open_ms [5] is_success(0/1) [6] is_probe(0/1)
--      [7] probe_success_target
--
-- Returns {state, open_until, backoff_ms, probe_successes, fail_score}.
--
-- Only a probe that holds K17 may count toward closing: an ordinary success
-- that happens to arrive during HALF_OPEN must not shorten the regression
-- condition. Any failure during HALF_OPEN re-opens with doubled backoff.
-- K23 (429 cooldown) is never consulted here; a quota response is not a
-- channel fault.

local key = KEYS[1]
local now_ms = tonumber(ARGV[1])
local threshold = tonumber(ARGV[2])
local base_open = tonumber(ARGV[3])
local max_open = tonumber(ARGV[4])
local is_success = ARGV[5] == "1"
local is_probe = ARGV[6] == "1"
local target = tonumber(ARGV[7])

local st = redis.call("HGET", key, "st") or "CLOSED"
local open_until = tonumber(redis.call("HGET", key, "open_until") or "0")
local bo = tonumber(redis.call("HGET", key, "bo") or "0")
if bo <= 0 then bo = base_open end
local ps = tonumber(redis.call("HGET", key, "ps") or "0")
local fc = tonumber(redis.call("HGET", key, "fc") or "0")

if st == "OPEN" then
  if now_ms < open_until then
    return {st, open_until, bo, ps, fc}
  end
  -- Lazy logical recovery: no timer is needed while the channel is idle.
  st = "HALF_OPEN"
  ps = 0
  redis.call("HSET", key, "st", st, "ps", 0)
end

if st == "HALF_OPEN" then
  if not is_success then
    local next_backoff = bo * 2
    if next_backoff < base_open then next_backoff = base_open end
    if next_backoff > max_open then next_backoff = max_open end
    open_until = now_ms + next_backoff
    redis.call("HSET", key, "st", "OPEN", "open_until", open_until,
               "bo", next_backoff, "ps", 0)
    return {"OPEN", open_until, next_backoff, 0, fc}
  end
  if is_probe then
    ps = tonumber(redis.call("HINCRBY", key, "ps", 1))
    if ps >= target then
      redis.call("HSET", key, "st", "CLOSED", "fc", 0, "open_until", 0,
                 "bo", base_open, "ps", 0)
      return {"CLOSED", 0, base_open, 0, 0}
    end
  end
  return {"HALF_OPEN", 0, bo, ps, fc}
end

if is_success then
  -- CLOSED success decays the score toward zero; it does not reset it, or a
  -- channel with a 50% failure rate could never trip.
  fc = tonumber(redis.call("HINCRBY", key, "fc", -1))
  if fc < 0 then
    redis.call("HSET", key, "fc", 0)
    fc = 0
  end
else
  fc = tonumber(redis.call("HINCRBY", key, "fc", 1))
  if fc >= threshold then
    bo = base_open
    open_until = now_ms + bo
    redis.call("HSET", key, "st", "OPEN", "open_until", open_until,
               "bo", bo, "ps", 0)
    return {"OPEN", open_until, bo, 0, fc}
  end
end

return {"CLOSED", 0, bo, 0, fc}
