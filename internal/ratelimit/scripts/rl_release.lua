-- L2 rl_release.lua — release the concurrency lease and compensate TPM/daily.
--
-- KEYS
--   [1] rl:cc:{kid}
--   [2] rl:tpm:{kid}:{min_at_acquire}
--   [3] rl:dq:{uid}:{day}
-- ARGV
--   [1] request_id
--   [2] tpm_delta (may be negative)
--   [3] dq_delta  (may be negative)
--   [4] acquire_min_epoch
--   [5] current_min_epoch
--   [6] require_member (1 = ZSET lease, 0 = concurrency-degraded path)
--
-- Returning {1, ZCARD} keeps the same shape on every path. ZREM of a missing
-- member is a no-op, so a second defer / the reconcile task cannot corrupt
-- the lease set.
--
-- When require_member=1 and ZREM removed nothing, this is a replay of a
-- release whose deltas were already applied; apply neither delta. The
-- degraded path sets require_member=0 because it never wrote K3.

local removed = redis.call("ZREM", KEYS[1], ARGV[1])
local apply = true
if ARGV[6] == "1" and removed == 0 then
  apply = false
end

if apply then
  local tpm_delta = tonumber(ARGV[2])
  local dq_delta = tonumber(ARGV[3])

  -- Cross-minute compensation is deliberately dropped: the acquire bucket
  -- may be gone, and TPM that is slightly too high is safer than TPM that is
  -- too low (TDS §2.2 L2).
  if ARGV[4] == ARGV[5] and tpm_delta ~= 0 then
    if redis.call("EXISTS", KEYS[2]) == 1 then
      local after = redis.call("INCRBY", KEYS[2], tpm_delta)
      -- An expired bucket must not be recreated, and a negative counter
      -- would exempt every later request. Correct it in place so the
      -- original PEXPIRE survives (Redis 5.0 has no SET ... KEEPTTL).
      if tonumber(after) < 0 then
        redis.call("INCRBY", KEYS[2], -tonumber(after))
      end
    end
  end

  if dq_delta ~= 0 and redis.call("EXISTS", KEYS[3]) == 1 then
    redis.call("INCRBY", KEYS[3], dq_delta)
  end
end

return {1, redis.call("ZCARD", KEYS[1])}
