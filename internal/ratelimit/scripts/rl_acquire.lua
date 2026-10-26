-- L1 rl_acquire.lua — four-dimension rate limit, one atomic decision.
--
-- KEYS
--   [1] rl:rpm:{kid}:{min}   [2] rl:tpm:{kid}:{min}
--   [3] rl:cc:{kid}          [4] rl:dq:{uid}:{day}
-- ARGV
--   [1] now_ms               [2] rpm_limit
--   [3] tpm_limit            [4] cc_limit
--   [5] dq_limit_micro       [6] est_tokens
--   [7] est_micro            [8] request_id
--   [9] lease_ms             [10] dq_enabled (0/1)
--   [11] min_epoch           [12] dq_reset_at_sec (next 00:00 UTC+8)
--   [13] cc_enabled (0/1)    [14] dq_ttl_ms
--
-- The first ten ARGV slots are the TDS §2.2 contract. Slots 11-14 are
-- append-only extensions needed by the 8-slot return contract and by the
-- concurrency-degraded path; they never renumber the TDS slots.
--
-- Order is fixed (T06 hard constraint 3): clean leases, read, evaluate
-- concurrency > RPM > TPM > daily, write ZADD first and only then K1/K2/K4.
-- Evaluating every dimension after the first hit is required for all_hit.
--
-- The return slots describe two different dimensions on purpose (T06 hard
-- constraint 4 / errata E-13): [2]code is the priority-first hit, while
-- [3]-[6] describe the hit that recovers last -- the one whose code the
-- outward 429 carries. If [3]-[6] followed [2], a four-dimension rejection
-- would tell the client "daily_quota_exceeded, retry in 3s".
--
-- Equal reset instants happen in real traffic: at 23:59 the RPM next_minute
-- equals the daily 00:00, and in the last 3s of any minute the concurrency
-- now+3s equals next_minute. Those ties are broken by `rank` using the same
-- order as OutwardCode() (daily 4 > RPM 3 > TPM 2 > concurrency 1), never by
-- the evaluation order, so [3]-[6] and the outward code cannot disagree.
--
-- Time is never read here. min_epoch, now_ms and the daily reset instant are
-- supplied by the caller (hard constraint 20).

local now_ms       = tonumber(ARGV[1])
local rpm_limit    = tonumber(ARGV[2])
local tpm_limit    = tonumber(ARGV[3])
local cc_limit     = tonumber(ARGV[4])
local dq_limit     = tonumber(ARGV[5])
local est_tokens   = tonumber(ARGV[6])
local est_micro    = tonumber(ARGV[7])
local request_id   = ARGV[8]
local lease_ms     = tonumber(ARGV[9])
local dq_enabled   = ARGV[10] == "1"
local min_epoch    = tonumber(ARGV[11])
local dq_reset_at  = tonumber(ARGV[12])
local cc_enabled   = ARGV[13] == "1"
local dq_ttl_ms    = tonumber(ARGV[14])

-- 1. lazy lease cleanup.
if cc_enabled then
  redis.call("ZREMRANGEBYSCORE", KEYS[3], "-inf", now_ms)
end

-- 2. read all four dimensions; a missing counter is zero.
local rpm_total = tonumber(redis.call("GET", KEYS[1]) or "0")
local tpm_total = tonumber(redis.call("GET", KEYS[2]) or "0")
local cc_total = 0
if cc_enabled then
  cc_total = tonumber(redis.call("ZCARD", KEYS[3]))
end
local dq_total = 0
if dq_enabled then
  dq_total = tonumber(redis.call("GET", KEYS[4]) or "0")
end

-- 3. evaluate every dimension. `hit` records the priority-first code in [2]
-- and the latest-recovering dimension in [3]-[6] / [8]. On equal reset
-- instants the higher `rank` wins, which is the OutwardCode() order
-- (daily > RPM > TPM > concurrency); evaluation order still decides [2].
local hits = {}
local main_code = ""
local main_limit, main_total, main_remaining, main_reset = 0, 0, 0, 0
local main_rank = 0
local reset_max = 0

local function hit(code, rank, limit, total, remaining, reset_at)
  hits[#hits + 1] = code
  if main_code == "" then
    main_code = code
  end
  if reset_at > reset_max or (reset_at == reset_max and rank > main_rank) then
    if reset_at > reset_max then
      reset_max = reset_at
    end
    main_limit = limit
    main_total = total
    main_remaining = remaining
    main_reset = reset_at
    main_rank = rank
  end
end

local next_minute = (min_epoch + 1) * 60

if cc_enabled and cc_total >= cc_limit then
  -- PRD §3.3: concurrency recovers after a fixed 3 seconds.
  hit("concurrency_limit_exceeded", 1, cc_limit, cc_total, 0, math.floor((now_ms + 3000) / 1000))
end
if rpm_total + 1 > rpm_limit then
  local remaining = rpm_limit - rpm_total
  if remaining < 0 then remaining = 0 end
  hit("rpm_limit_exceeded", 3, rpm_limit, rpm_total, remaining, next_minute)
end
if tpm_total + est_tokens > tpm_limit then
  local remaining = tpm_limit - tpm_total
  if remaining < 0 then remaining = 0 end
  hit("tpm_limit_exceeded", 2, tpm_limit, tpm_total, remaining, next_minute)
end
if dq_enabled and dq_total + est_micro > dq_limit then
  local remaining = dq_limit - dq_total
  if remaining < 0 then remaining = 0 end
  hit("daily_quota_exceeded", 4, dq_limit, dq_total, remaining, dq_reset_at)
end

if #hits > 0 then
  return {0, main_code, main_limit, main_total, main_remaining, main_reset,
          table.concat(hits, ","), reset_max}
end

-- 4. write. ZADD first so a later write failure cannot leave a request
-- holding an RPM/TPM charge without a concurrency lease.
if cc_enabled then
  redis.call("ZADD", KEYS[3], now_ms + lease_ms, request_id)
end

local rpm_after = redis.call("INCRBY", KEYS[1], 1)
if rpm_after == 1 then
  redis.call("PEXPIRE", KEYS[1], 130000)
end

local tpm_after = redis.call("INCRBY", KEYS[2], est_tokens)
if tpm_after == est_tokens then
  redis.call("PEXPIRE", KEYS[2], 130000)
end

if dq_enabled then
  local dq_after = redis.call("INCRBY", KEYS[4], est_micro)
  if dq_after == est_micro then
    redis.call("PEXPIRE", KEYS[4], dq_ttl_ms)
  end
end

-- The allowed branch has no rejected dimension, so slots 3-6 are zero by
-- the TDS return contract ("only the rejected dimension carries values").
return {1, "", 0, 0, 0, 0, "", 0}
