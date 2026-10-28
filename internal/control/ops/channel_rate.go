package ops

import (
	"context"
	"database/sql"
	"time"
)

// channelSuccessWindow is the operator channel health window. The denominator
// is upstream attempts (t_upstream_attempt), not requests: a retry belongs to
// the same user request but is a separate attempt against the channel.
const channelSuccessWindow = 15 * time.Minute

type channelRateQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// attachChannelRates fills success_rate_bp for a page of channels in one
// GROUP BY query. idx_ua_channel_created (channel_id, created_at) serves the
// IN list plus the 15-minute range.
func (s *Service) attachChannelRates(ctx context.Context, q channelRateQuerier, items []channelView) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	args := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}
	now := s.now()
	args = append(args, now.Add(-channelSuccessWindow), now)

	rows, err := q.QueryContext(ctx, `
		SELECT channel_id, COUNT(*),
		       CAST(COALESCE(SUM(result = 'SUCCESS'), 0) AS SIGNED)
		  FROM t_upstream_attempt FORCE INDEX (idx_ua_channel_created)
		 WHERE channel_id IN (`+placeholders(len(ids))+`)
		   AND created_at >= ? AND created_at <= ?
		 GROUP BY channel_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	type attemptAgg struct {
		total   int64
		success int64
	}
	byChannel := make(map[int64]attemptAgg, len(items))
	for rows.Next() {
		var (
			channelID int64
			agg       attemptAgg
		)
		if err := rows.Scan(&channelID, &agg.total, &agg.success); err != nil {
			return err
		}
		byChannel[channelID] = agg
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for i := range items {
		agg, ok := byChannel[items[i].ID]
		if !ok || agg.total <= 0 {
			continue
		}
		bp := agg.success * 10000 / agg.total
		items[i].SuccessRateBP = &bp
	}
	return nil
}
