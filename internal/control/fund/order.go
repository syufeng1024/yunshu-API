package fund

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/redisx"
)

// Order types and states (t_recharge_order.order_type / .state, TDS §1.5).
const (
	OrderTypeCustom = "CUSTOM"
	OrderTypePlan   = "PLAN"

	OrderStatePending         = "PENDING"
	OrderStatePaid            = "PAID"
	OrderStateClosed          = "CLOSED"
	OrderStateRefunding       = "REFUNDING"
	OrderStateRefunded        = "REFUNDED"
	OrderStatePartialRefunded = "PARTIAL_REFUNDED"
)

// Pay channels. OFFLINE (对公转账) is deliberately absent: it is settled by a
// finance operator, and B-46 A 档 only wires WECHAT and ALIPAY to a callback.
const (
	PayChannelWechat = "WECHAT"
	PayChannelAlipay = "ALIPAY"
)

// orderStates is the complete t_recharge_order.state enumeration. It is the
// one place the code lists the states; parseOrderStatus and the docs
// consistency test both derive from it, so a state added to the DDL and here
// cannot leave the document behind.
var orderStates = []string{
	OrderStatePending,
	OrderStatePaid,
	OrderStateClosed,
	OrderStateRefunding,
	OrderStateRefunded,
	OrderStatePartialRefunded,
}

// idemScopeOrder namespaces the console order Idempotency-Key inside K6. The
// gateway's scopes are different strings, so the two never read each other's
// records even though the key function is shared.
const idemScopeOrder = "console_order"

// idemPending marks "a request with this key is in flight".
const idemPending = "pending"

type createOrderRequest struct {
	OrderType string `json:"order_type"`
	PlanID    *int64 `json:"plan_id"`
	// PayAmountFen is required for CUSTOM and, when supplied for PLAN, must
	// equal the tier price: accepting a lower amount would sell a plan below
	// its configured price.
	PayAmountFen   *int64 `json:"pay_amount_fen"`
	PayChannel     string `json:"pay_channel"`
	IdempotencyKey string `json:"idempotency_key"`
}

type orderView struct {
	ID            int64      `json:"id"`
	OrderNo       string     `json:"order_no"`
	OrderType     string     `json:"order_type"`
	PlanCode      string     `json:"plan_code,omitempty"`
	PayAmountFen  int64      `json:"pay_amount_fen"`
	PayAmountYuan int64      `json:"pay_amount_yuan"`
	RechargeMicro int64      `json:"recharge_micro"`
	BonusMicro    int64      `json:"bonus_micro"`
	Recharge      int64      `json:"recharge"`
	Bonus         int64      `json:"bonus"`
	PayChannel    string     `json:"pay_channel"`
	State         string     `json:"state"`
	PaidAt        *time.Time `json:"paid_at"`
	ExpireAt      time.Time  `json:"expire_at"`
	CreatedAt     time.Time  `json:"created_at"`

	// planID is carried for internal lookups (plan code, entitlement) and is
	// never serialized.
	planID *int64
}

type createOrderResponse struct {
	Order orderView `json:"order"`
	// Replayed is true when the same Idempotency-Key returned the order that
	// the first request created. No second row exists in that case.
	Replayed bool `json:"idempotent_replay"`
}

// CreateOrder implements POST /api/console/orders (F10-1 / §5.6).
//
// The order is created in PENDING and grants nothing: credits move only when
// the payment callback arrives (hard constraint 2). Amounts are computed
// server-side from t_plan for PLAN orders, so a client cannot buy a plan for
// one fen.
func (s *Service) CreateOrder(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req createOrderRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ctx := r.Context()

	orderType := strings.ToUpper(strings.TrimSpace(req.OrderType))
	if orderType != OrderTypeCustom && orderType != OrderTypePlan {
		mw.WriteError(w, r, mw.InvalidField("order_type"))
		return
	}
	channel := strings.ToUpper(strings.TrimSpace(req.PayChannel))
	if channel != PayChannelWechat && channel != PayChannelAlipay {
		mw.WriteError(w, r, mw.InvalidField("pay_channel"))
		return
	}

	var (
		planID        *int64
		planCode      string
		payAmountFen  int64
		rechargeMicro int64
		bonusMicro    int64
		bonusDays     int64
	)
	switch orderType {
	case OrderTypePlan:
		if req.PlanID == nil || *req.PlanID <= 0 {
			mw.WriteError(w, r, mw.InvalidField("plan_id"))
			return
		}
		plan, apiErr := s.findPlan(ctx, *req.PlanID)
		if apiErr != nil {
			mw.WriteError(w, r, apiErr)
			return
		}
		if !plan.Purchasable {
			// hard constraint 8: the free default and the trial tier are
			// granted, never sold.
			mw.WriteError(w, r, mw.InvalidField("plan_id"))
			return
		}
		if req.PayAmountFen != nil && *req.PayAmountFen != plan.PriceFen {
			mw.WriteError(w, r, mw.InvalidField("pay_amount_fen"))
			return
		}
		payAmountFen = plan.PriceFen
		rechargeMicro = plan.GrantRechargeMicro
		bonusMicro = plan.GrantBonusMicro
		bonusDays = plan.BonusValidDays
		id := plan.ID
		planID = &id
		planCode = plan.Code
	case OrderTypeCustom:
		if req.PlanID != nil {
			mw.WriteError(w, r, mw.InvalidField("plan_id"))
			return
		}
		if req.PayAmountFen == nil {
			mw.WriteError(w, r, mw.InvalidField("pay_amount_fen"))
			return
		}
		fen := *req.PayAmountFen
		if fen < 1 || fen > customRechargeMaxFen {
			mw.WriteError(w, r, mw.InvalidField("pay_amount_fen"))
			return
		}
		payAmountFen = fen
		// PRD §5.6: 1 元 = 100 积分 and a custom top-up carries no bonus, so
		// the whole credit is refundable.
		rechargeMicro = fenToMicro(fen)
		bonusMicro = 0
	}
	if bonusMicro > 0 && rechargeMicro <= 0 {
		// A bonus-only purchase has no refundable part and no price anchor;
		// t_plan never produces it, so treat it as a data defect.
		s.logError(ctx, "fund_order_bonus_without_recharge plan="+planCode)
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	idemKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idemKey == "" {
		idemKey = strings.TrimSpace(req.IdempotencyKey)
	}
	if len(idemKey) > 128 {
		mw.WriteError(w, r, mw.InvalidField("idempotency_key"))
		return
	}

	view, replayed, apiErr := s.insertOrder(ctx, r, uid, idemKey, orderInput{
		OrderType:     orderType,
		PlanID:        planID,
		PlanCode:      planCode,
		PayAmountFen:  payAmountFen,
		RechargeMicro: rechargeMicro,
		BonusMicro:    bonusMicro,
		BonusDays:     bonusDays,
		PayChannel:    channel,
	})
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	_ = mw.WriteJSON(w, status, createOrderResponse{Order: view, Replayed: replayed})
}

type orderInput struct {
	OrderType     string
	PlanID        *int64
	PlanCode      string
	PayAmountFen  int64
	RechargeMicro int64
	BonusMicro    int64
	BonusDays     int64
	PayChannel    string
}

// insertOrder writes the PENDING order and its audit row, guarded by the
// Idempotency-Key window.
//
// The window is a Redis reservation, not a "select then insert": the first
// request claims the key with SET NX, and a concurrent duplicate either sees
// the reservation (request_in_flight) or the finished order id (replay). The
// database keeps uk_ro_order_no as the last line of defence.
func (s *Service) insertOrder(ctx context.Context, r *http.Request, uid int64, idemKey string, in orderInput) (orderView, bool, *apierr.APIError) {
	now := s.now()
	window := s.idemWindowDuration(ctx)
	reservation := ""

	if idemKey != "" && s.Redis != nil {
		hash := redisx.IdemHash(idemScopeOrder, 0, idemKey)
		key := redisx.IdemKey(uid, hash)
		cctx, cancel := s.Redis.CommandContext(ctx)
		ok, err := s.Redis.Raw().SetNX(cctx, key, idemPending, window).Result()
		cancel()
		if err != nil {
			// Redis is not the authority for money: log and continue. The
			// unique order number still prevents duplicate rows from a retry
			// storm only imperfectly, so the failure is loud.
			s.logWarn(ctx, "fund_order_idem_reserve_failed: "+err.Error())
		} else if !ok {
			existing, apiErr := s.readIdemOrder(ctx, uid, hash, idemKey)
			if apiErr != nil {
				return orderView{}, false, apiErr
			}
			if existing != nil {
				return *existing, true, nil
			}
			// Reserved but not yet resolved: the first request is in flight.
			return orderView{}, false, apierr.New(apierr.CodeRequestInFlight)
		}
		reservation = key
	}

	releaseReservation := func() {
		if reservation == "" {
			return
		}
		cctx, cancel := s.Redis.CommandContext(ctx)
		_ = s.Redis.Raw().Del(cctx, reservation).Err()
		cancel()
	}

	var (
		orderID int64
		orderNO string
		err     error
	)
	expireAt := now.Add(orderExpiryMinutes * time.Minute)
	for attempt := 0; attempt < 5; attempt++ {
		orderNO = orderNo(now, randomDigits)
		var planArg any
		if in.PlanID != nil {
			planArg = *in.PlanID
		}
		res, ierr := s.DB.ExecContext(ctx, `
			INSERT INTO t_recharge_order
				(order_no, user_id, order_type, plan_id, pay_amount_fen,
				 recharge_micro, bonus_micro, pay_channel, state,
				 expire_at, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?, 'PENDING', ?,?,?)`,
			orderNO, uid, in.OrderType, planArg, in.PayAmountFen,
			in.RechargeMicro, in.BonusMicro, in.PayChannel,
			expireAt, now, now)
		if ierr == nil {
			orderID, err = res.LastInsertId()
			break
		}
		err = ierr
		if !isDuplicateKey(ierr) {
			break
		}
	}
	if err != nil || orderID == 0 {
		releaseReservation()
		if err != nil {
			s.logError(ctx, "fund_order_insert_failed: "+err.Error())
		}
		return orderView{}, false, apierr.New(apierr.CodeInternalError)
	}

	after, merr := jsonx.Marshal(map[string]any{
		"order_no":       orderNO,
		"order_type":     in.OrderType,
		"plan_code":      in.PlanCode,
		"pay_amount_fen": in.PayAmountFen,
		"recharge_micro": in.RechargeMicro,
		"bonus_micro":    in.BonusMicro,
		"pay_channel":    in.PayChannel,
		"state":          OrderStatePending,
		"expire_at":      expireAt,
	})
	if merr == nil {
		if aerr := s.Audit.Insert(ctx, s.DB, audit.Entry{
			OperatorID:   uid,
			OperatorRole: "PERSONAL",
			OperatorIP:   audit.IPBytes(r),
			Module:       "FUND",
			Action:       "ORDER_CREATE",
			TargetType:   "t_recharge_order",
			TargetID:     itoa(orderID),
			After:        after,
			Result:       "OK",
		}); aerr != nil {
			// The order exists and is payable; a missing audit row must not
			// strand the user's payment. Log loudly instead.
			s.logWarn(ctx, "fund_order_audit_failed: "+aerr.Error())
		}
	}

	if reservation != "" {
		cctx, cancel := s.Redis.CommandContext(ctx)
		_ = s.Redis.Raw().Set(cctx, reservation, itoa(orderID), window).Err()
		cancel()
	}

	view, apiErr := s.orderByID(ctx, uid, orderID, in.PlanCode)
	if apiErr != nil {
		return orderView{}, false, apiErr
	}
	return view, false, nil
}

// readIdemOrder resolves a reservation: nil means "still in flight".
func (s *Service) readIdemOrder(ctx context.Context, uid int64, hash, idemKey string) (*orderView, *apierr.APIError) {
	cctx, cancel := s.Redis.CommandContext(ctx)
	value, err := s.Redis.Raw().Get(cctx, redisx.IdemKey(uid, hash)).Result()
	cancel()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			// The window expired between SET NX failing and this read.
			return nil, apierr.New(apierr.CodeRequestInFlight)
		}
		s.logWarn(ctx, "fund_order_idem_read_failed: "+err.Error())
		return nil, apierr.New(apierr.CodeRequestInFlight)
	}
	if value == idemPending || value == "" {
		return nil, nil
	}
	id, perr := strconv.ParseInt(value, 10, 64)
	if perr != nil {
		// An unreadable reservation is a cache defect, not a user error.
		s.logWarn(ctx, "fund_order_idem_value_invalid: "+value)
		return nil, nil
	}
	view, apiErr := s.orderByID(ctx, uid, id, "")
	if apiErr != nil {
		return nil, nil
	}
	return &view, nil
}

const orderColumns = `
	id, order_no, order_type, plan_id, pay_amount_fen, recharge_micro,
	bonus_micro, pay_channel, state, paid_at, expire_at, created_at`

// orderByID reads one order of the given account.
func (s *Service) orderByID(ctx context.Context, uid, id int64, planCode string) (orderView, *apierr.APIError) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+orderColumns+`
		  FROM t_recharge_order WHERE id = ? AND user_id = ?`, id, uid)
	v, err := scanOrder(row)
	if errors.Is(err, sql.ErrNoRows) {
		// No public endpoint reads an order by id in this segment, so a
		// missing row means the row this process just wrote is gone: an
		// internal inconsistency, not a 404 the client can act on.
		s.logError(ctx, "fund_order_missing: id="+itoa(id))
		return orderView{}, apierr.New(apierr.CodeInternalError)
	}
	if err != nil {
		s.logError(ctx, "fund_order_read_failed: "+err.Error())
		return orderView{}, apierr.New(apierr.CodeInternalError)
	}
	v.PlanCode = planCode
	if v.PlanCode == "" && v.planID != nil {
		var code string
		if err := s.DB.QueryRowContext(ctx,
			`SELECT code FROM t_plan WHERE id = ?`, *v.planID).Scan(&code); err == nil {
			v.PlanCode = code
		}
	}
	return v, nil
}

func scanOrder(sc scanner) (orderView, error) {
	var (
		v      orderView
		planID sql.NullInt64
		paidAt sql.NullTime
	)
	if err := sc.Scan(&v.ID, &v.OrderNo, &v.OrderType, &planID, &v.PayAmountFen,
		&v.RechargeMicro, &v.BonusMicro, &v.PayChannel, &v.State,
		&paidAt, &v.ExpireAt, &v.CreatedAt); err != nil {
		return orderView{}, err
	}
	if planID.Valid {
		id := planID.Int64
		v.planID = &id
	}
	if paidAt.Valid {
		t := paidAt.Time.UTC()
		v.PaidAt = &t
	}
	v.PayAmountYuan = v.PayAmountFen / FenPerYuan
	v.Recharge = microToFen(v.RechargeMicro) / FenPerYuan
	v.Bonus = microToFen(v.BonusMicro) / FenPerYuan
	v.ExpireAt = v.ExpireAt.UTC()
	v.CreatedAt = v.CreatedAt.UTC()
	return v, nil
}

type orderListResponse struct {
	Items    []orderView `json:"items"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
	Total    int64       `json:"total"`
}

// ListOrders implements GET /api/console/orders (F10-4). The status filter and
// the order-number search intersect, the same way the key list does.
func (s *Service) ListOrders(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	ctx := r.Context()
	page, apiErr := mw.ParsePage(r, "created_at", "paid_at")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) > 32 {
		mw.WriteError(w, r, mw.InvalidQueryField("q"))
		return
	}
	states, apiErr := parseOrderStatus(r.URL.Query().Get("status"))
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	where := []string{"user_id = ?"}
	args := []any{uid}
	if q != "" {
		where = append(where, `order_no LIKE ? ESCAPE '!'`)
		args = append(args, likePattern(q))
	}
	if len(states) == 1 {
		where = append(where, "state = ?")
		args = append(args, states[0])
	} else if len(states) > 1 {
		placeholders := make([]string, len(states))
		for i, st := range states {
			placeholders[i] = "?"
			args = append(args, st)
		}
		where = append(where, "state IN ("+strings.Join(placeholders, ",")+")")
	}
	whereSQL := strings.Join(where, " AND ")

	var total int64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_recharge_order WHERE `+whereSQL, args...).Scan(&total); err != nil {
		s.logError(ctx, "fund_order_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	orderBy := "created_at"
	if page.Sort == "paid_at" {
		orderBy = "paid_at"
	}
	direction := "DESC"
	if strings.EqualFold(page.Order, "asc") {
		direction = "ASC"
	}
	queryArgs := append(append([]any{}, args...), page.Size, page.OffsetInt64())
	rows, err := s.DB.QueryContext(ctx, `SELECT `+orderColumns+`
		  FROM t_recharge_order WHERE `+whereSQL+`
		  ORDER BY `+orderBy+` `+direction+`, id `+direction+`
		  LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		s.logError(ctx, "fund_order_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()
	items := make([]orderView, 0, page.Size)
	for rows.Next() {
		v, err := scanOrder(rows)
		if err != nil {
			s.logError(ctx, "fund_order_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "fund_order_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, orderListResponse{
		Items: items, Page: page.Number, PageSize: page.Size, Total: total,
	})
}

// parseOrderStatus maps the console filter to t_recharge_order.state values.
// An unknown filter is a query error, never "no filter": silently widening a
// query would show a different list than the one the user asked for.
func parseOrderStatus(raw string) ([]string, *apierr.APIError) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "all":
		return nil, nil
	case "pending":
		return []string{OrderStatePending}, nil
	case "paid":
		return []string{OrderStatePaid}, nil
	case "closed":
		return []string{OrderStateClosed}, nil
	case "refunding":
		return []string{OrderStateRefunding}, nil
	case "refunded":
		return []string{OrderStateRefunded, OrderStatePartialRefunded}, nil
	default:
		return nil, mw.InvalidQueryField("status")
	}
}
