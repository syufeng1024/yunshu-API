package ratelimit

import "time"

// cst is UTC+8 without daylight saving. Asia/Shanghai has observed no DST
// since 1991; a fixed zone avoids the tzdata dependency while producing the
// same yyyymmdd and 00:00 boundary as time.LoadLocation("Asia/Shanghai").
var cst = time.FixedZone("CST", 8*60*60)

// Day returns the {yyyymmdd_cst} segment of K4/K5. UTC dates are forbidden:
// a request at 23:30 UTC is already the next day in Shanghai and must land in
// the new key.
func Day(t time.Time) string { return t.In(cst).Format("20060102") }

// NextReset returns the next 00:00 Asia/Shanghai boundary. It is the
// reset_at value reported for daily_quota_exceeded and the reset_at_max
// winner when the daily dimension is among several hits.
func NextReset(t time.Time) time.Time {
	local := t.In(cst)
	year, month, day := local.Date()
	return time.Date(year, month, day+1, 0, 0, 0, 0, cst)
}

// NextResetUnix is NextReset in unix seconds.
func NextResetUnix(t time.Time) int64 { return NextReset(t).Unix() }

// TTL returns the K4/K5 lifetime: seconds until 00:00 Asia/Shanghai plus the
// 600s grace from T06 hard constraint 9 and TDS §2.2 L2, so the key expires at
// 00:10. Counting to 00:10 and then adding the grace again would place expiry
// at 00:20 (errata E-11) and widen the late-compensation window past the
// reconciliation cutoff.
func TTL(t time.Time) time.Duration {
	local := t.In(cst)
	year, month, day := local.Date()
	untilMidnight := time.Date(year, month, day+1, 0, 0, 0, 0, cst).Sub(local)
	return untilMidnight + 600*time.Second
}

// TTLMillis is TTL in milliseconds for PEXPIRE.
func TTLMillis(t time.Time) int64 { return TTL(t).Milliseconds() }
