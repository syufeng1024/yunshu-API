package fund

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/mw"
)

// Redeem-code batches (B-44 A 档 / F10-3).
//
// A finance operator buys codes in bulk — one batch, one face value, one
// validity window — and hands the plaintext to customers (printed cards,
// e-mail campaigns). The plaintext exists exactly twice: in the generator's
// response and in the customer's hand. The database keeps only
// SHA256(plaintext) plus a six-character prefix for display, so a dump of
// t_redeem_code cannot be redeemed.
//
// The generation and the redemption therefore share one contract, and this
// file derives its code shape from the redemption side rather than inventing
// one: fund.Redeem upper-cases and trims the input, then requires 6..64
// characters and looks the value up by SHA256 hex. Every code generated here
// is 16 characters of an unambiguous alphabet, well inside that window.

const (
	// redeemBatchMinQuantity / redeemBatchMaxQuantity bound one generation
	// call. The ceiling keeps a single transaction (and the operator's
	// copy-paste) bounded; a larger campaign is several batches.
	redeemBatchMinQuantity = 1
	redeemBatchMaxQuantity = 500

	// redeemBatchMaxFaceValueFen comes from the column type:
	// t_redeem_batch.face_value_fen is smallint unsigned, so 65535 is the
	// largest value the schema can store.
	redeemBatchMaxFaceValueFen int64 = 65535

	// Redeem validity window, in days.
	redeemValidDaysMin int64 = 1
	redeemValidDaysMax int64 = 3650

	// redeemBatchNameMax follows t_redeem_batch.batch_name varchar(64).
	redeemBatchNameMax = 64

	// redeemCodeLength is the plaintext length. 16 characters over a
	// 31-symbol alphabet is ~79 bits: guessing a code is not a strategy, and
	// 16 is short enough to retype from a card.
	redeemCodeLength = 16

	// redeemCodeCharset omits 0/O and 1/I/L. Redeem upper-cases the input,
	// so excluding L also covers a typed lowercase l. A customer reads these
	// off a printed card or a phone screen.
	redeemCodeCharset = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

	// redeemCodeInsertChunk keeps one INSERT under 1000 placeholders.
	redeemCodeInsertChunk = 100

	// redeemCodeGenerateAttempts bounds the regenerate-on-collision loop.
	// uk_rc_hash makes a collision detectable; with a 79-bit space it is a
	// defect indicator rather than a routine event.
	redeemCodeGenerateAttempts = 5
)

// Credit targets a batch can pay into (t_redeem_batch.credit_target).
const (
	redeemTargetRecharge = "RECHARGE"
	redeemTargetBonus    = "BONUS"
)

type createRedeemBatchRequest struct {
	BatchName    string `json:"batch_name"`
	FaceValueFen *int64 `json:"face_value_fen"`
	Quantity     *int64 `json:"quantity"`
	CreditTarget string `json:"credit_target"`
	ValidDays    *int64 `json:"valid_days"`
}

type redeemBatchView struct {
	ID             int64     `json:"id"`
	BatchName      string    `json:"batch_name"`
	FaceValueFen   int64     `json:"face_value_fen"`
	FaceValueYuan  int64     `json:"face_value_yuan"`
	Quantity       int64     `json:"quantity"`
	GeneratedCount int64     `json:"generated_count"`
	RedeemedCount  int64     `json:"redeemed_count"`
	ValidFrom      time.Time `json:"valid_from"`
	ValidTo        time.Time `json:"valid_to"`
	CreditTarget   string    `json:"credit_target"`
	CreatedBy      int64     `json:"created_by"`
	CreatedByEmail string    `json:"created_by_email,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type createRedeemBatchResponse struct {
	Batch redeemBatchView `json:"batch"`
	// Codes is the one and only time the plaintext leaves the server. It is
	// not stored, not logged and not audited; leaving the page loses it.
	Codes []string `json:"codes"`
}

type redeemBatchListResponse struct {
	Items    []redeemBatchView `json:"items"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
	Total    int64             `json:"total"`
}

// CreateRedeemBatch implements POST /api/admin/redeem-codes/batch
// (fund.approve: issuing credit is a money act, so OPS is refused).
func (s *Service) CreateRedeemBatch(w http.ResponseWriter, r *http.Request) {
	operatorID, operatorRole, apiErr := actorOf(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	var req createRedeemBatchRequest
	if apiErr := decodeJSON(w, r, &req); apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	name := strings.TrimSpace(req.BatchName)
	if name == "" || len([]rune(name)) > redeemBatchNameMax {
		mw.WriteError(w, r, mw.InvalidField("batch_name"))
		return
	}
	if req.FaceValueFen == nil || *req.FaceValueFen < 1 ||
		*req.FaceValueFen > redeemBatchMaxFaceValueFen {
		mw.WriteError(w, r, mw.InvalidField("face_value_fen"))
		return
	}
	if req.Quantity == nil || *req.Quantity < redeemBatchMinQuantity ||
		*req.Quantity > redeemBatchMaxQuantity {
		mw.WriteError(w, r, mw.InvalidField("quantity"))
		return
	}
	target := strings.ToUpper(strings.TrimSpace(req.CreditTarget))
	if target == "" {
		// The column default is BONUS (B-44 A 档: 兑换积分不计退款额度).
		target = redeemTargetBonus
	}
	if target != redeemTargetRecharge && target != redeemTargetBonus {
		mw.WriteError(w, r, mw.InvalidField("credit_target"))
		return
	}
	if req.ValidDays == nil || *req.ValidDays < redeemValidDaysMin ||
		*req.ValidDays > redeemValidDaysMax {
		mw.WriteError(w, r, mw.InvalidField("valid_days"))
		return
	}

	resp, apiErr := s.createRedeemBatchTx(r.Context(), r, operatorID, operatorRole, redeemBatchInput{
		BatchName:    name,
		FaceValueFen: *req.FaceValueFen,
		Quantity:     *req.Quantity,
		CreditTarget: target,
		ValidDays:    *req.ValidDays,
	})
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	_ = mw.WriteJSON(w, http.StatusCreated, resp)
}

type redeemBatchInput struct {
	BatchName    string
	FaceValueFen int64
	Quantity     int64
	CreditTarget string
	ValidDays    int64
}

// createRedeemBatchTx writes the batch, its codes and the audit row in one
// transaction: a batch whose codes failed to insert would show an issued
// quantity that does not exist.
func (s *Service) createRedeemBatchTx(ctx context.Context, r *http.Request, operatorID int64, operatorRole string, in redeemBatchInput) (createRedeemBatchResponse, *apierr.APIError) {
	now := s.now()
	validTo := now.Add(time.Duration(in.ValidDays) * 24 * time.Hour)

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		s.logError(ctx, "fund_redeem_batch_begin_failed: "+err.Error())
		return createRedeemBatchResponse{}, apierr.New(apierr.CodeInternalError)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	res, err := tx.ExecContext(ctx, `
		INSERT INTO t_redeem_batch
			(batch_name, face_value_fen, quantity, generated_count, redeemed_count,
			 valid_from, valid_to, credit_target, created_by, created_at)
		VALUES (?,?,?,?,0,?,?,?,?,?)`,
		in.BatchName, in.FaceValueFen, in.Quantity, in.Quantity,
		now, validTo, in.CreditTarget, operatorID, now)
	if err != nil {
		s.logError(ctx, "fund_redeem_batch_insert_failed: "+err.Error())
		return createRedeemBatchResponse{}, apierr.New(apierr.CodeInternalError)
	}
	batchID, err := res.LastInsertId()
	if err != nil {
		s.logError(ctx, "fund_redeem_batch_id_failed: "+err.Error())
		return createRedeemBatchResponse{}, apierr.New(apierr.CodeInternalError)
	}

	codes, apiErr := s.insertRedeemCodesTx(ctx, tx, batchID, in.Quantity, validTo, now)
	if apiErr != nil {
		return createRedeemBatchResponse{}, apiErr
	}

	if aerr := s.Audit.Insert(ctx, tx, audit.Entry{
		OperatorID:   operatorID,
		OperatorRole: operatorRole,
		OperatorIP:   audit.IPBytes(r),
		Module:       "FUND",
		Action:       "REDEEM_BATCH_CREATE",
		TargetType:   "t_redeem_batch",
		TargetID:     itoa(batchID),
		// Hard rule: the plaintext never reaches the audit trail. The prefix
		// is the masked form the database itself keeps.
		After: mustJSON(map[string]any{
			"batch_name":     in.BatchName,
			"face_value_fen": in.FaceValueFen,
			"quantity":       in.Quantity,
			"credit_target":  in.CreditTarget,
			"valid_from":     now,
			"valid_to":       validTo,
		}),
		Result: "OK",
	}); aerr != nil {
		s.logError(ctx, "fund_redeem_batch_audit_failed: "+aerr.Error())
		return createRedeemBatchResponse{}, apierr.New(apierr.CodeInternalError)
	}

	if err := tx.Commit(); err != nil {
		s.logError(ctx, "fund_redeem_batch_commit_failed: "+err.Error())
		return createRedeemBatchResponse{}, apierr.New(apierr.CodeInternalError)
	}

	return createRedeemBatchResponse{
		Batch: redeemBatchView{
			ID:             batchID,
			BatchName:      in.BatchName,
			FaceValueFen:   in.FaceValueFen,
			FaceValueYuan:  in.FaceValueFen / FenPerYuan,
			Quantity:       in.Quantity,
			GeneratedCount: in.Quantity,
			RedeemedCount:  0,
			ValidFrom:      now,
			ValidTo:        validTo,
			CreditTarget:   in.CreditTarget,
			CreatedBy:      operatorID,
			CreatedAt:      now,
		},
		Codes: codes,
	}, nil
}

// insertRedeemCodesTx writes quantity codes and returns their plaintext.
//
// On a uk_rc_hash collision the already-written rows of this batch are
// deleted and the whole set is regenerated: a partial batch would break the
// batch's own quantity invariant, and the transaction has not committed, so
// throwing the attempt away costs nothing.
func (s *Service) insertRedeemCodesTx(ctx context.Context, tx *sql.Tx, batchID, quantity int64, validTo, now time.Time) ([]string, *apierr.APIError) {
	for attempt := 0; attempt < redeemCodeGenerateAttempts; attempt++ {
		codes := make([]string, 0, quantity)
		hashes := make([]string, 0, quantity)
		for i := int64(0); i < quantity; i++ {
			plain, err := randomRedeemCode()
			if err != nil {
				s.logError(ctx, "fund_redeem_code_rand_failed: "+err.Error())
				return nil, apierr.New(apierr.CodeInternalError)
			}
			codes = append(codes, plain)
			sum := sha256.Sum256([]byte(plain))
			hashes = append(hashes, hex.EncodeToString(sum[:]))
		}

		if err := insertCodeRows(ctx, tx, batchID, codes, hashes, validTo, now); err != nil {
			if !isDuplicateKey(err) {
				s.logError(ctx, "fund_redeem_code_insert_failed: "+err.Error())
				return nil, apierr.New(apierr.CodeInternalError)
			}
			s.logWarn(ctx, "fund_redeem_code_collision attempt="+itoa(int64(attempt)))
			if _, derr := tx.ExecContext(ctx,
				`DELETE FROM t_redeem_code WHERE batch_id = ?`, batchID); derr != nil {
				s.logError(ctx, "fund_redeem_code_collision_cleanup_failed: "+derr.Error())
				return nil, apierr.New(apierr.CodeInternalError)
			}
			continue
		}
		return codes, nil
	}
	// Five consecutive collisions on a 31^16 space is not bad luck: something
	// is systematically wrong (a stuck entropy source, a broken hash column).
	s.logError(ctx, "fund_redeem_code_generate_exhausted batch="+itoa(batchID))
	return nil, apierr.New(apierr.CodeInternalError)
}

// insertCodeRows writes one chunked multi-row INSERT. Only the hash and the
// six-character display prefix are persisted; the plaintext stays in memory.
func insertCodeRows(ctx context.Context, tx *sql.Tx, batchID int64, codes, hashes []string, validTo, now time.Time) error {
	for start := 0; start < len(codes); start += redeemCodeInsertChunk {
		end := start + redeemCodeInsertChunk
		if end > len(codes) {
			end = len(codes)
		}
		var sb strings.Builder
		sb.WriteString(`INSERT INTO t_redeem_code
			(batch_id, code_hash, code_prefix, status, valid_to, created_at) VALUES `)
		args := make([]any, 0, (end-start)*6)
		for i := start; i < end; i++ {
			if i > start {
				sb.WriteString(",")
			}
			sb.WriteString("(?,?,?,'UNUSED',?,?)")
			args = append(args, batchID, hashes[i], codePrefix(codes[i]), validTo, now)
		}
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

// codePrefix is the six-character masked form t_redeem_code.code_prefix holds
// (char(6) NOT NULL). Redeem requires at least six characters, so the slice is
// always full length for a generated code.
func codePrefix(plain string) string {
	if len(plain) <= 6 {
		return plain
	}
	return plain[:6]
}

// randomRedeemCode draws redeemCodeLength symbols from redeemCodeCharset with
// rejection sampling, so no symbol is favoured by a modulo bias.
func randomRedeemCode() (string, error) {
	n := len(redeemCodeCharset)
	// 256 mod n symbols would be over-represented; values at or above the
	// cutoff are discarded.
	cutoff := byte(256 - (256 % n))
	out := make([]byte, 0, redeemCodeLength)
	buf := make([]byte, redeemCodeLength*2)
	for len(out) < redeemCodeLength {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b >= cutoff {
				continue
			}
			out = append(out, redeemCodeCharset[int(b)%n])
			if len(out) == redeemCodeLength {
				break
			}
		}
	}
	return string(out), nil
}

// ListRedeemBatches implements GET /api/admin/redeem-codes (fund.read).
func (s *Service) ListRedeemBatches(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, apiErr := mw.ParsePage(r, "created_at", "valid_to")
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	var total int64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_redeem_batch`).Scan(&total); err != nil {
		s.logError(ctx, "fund_redeem_batch_count_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}

	orderBy := "b.created_at"
	if page.Sort == "valid_to" {
		orderBy = "b.valid_to"
	}
	direction := "DESC"
	if strings.EqualFold(page.Order, "asc") {
		direction = "ASC"
	}

	rows, err := s.DB.QueryContext(ctx, `
		SELECT b.id, b.batch_name, b.face_value_fen, b.quantity,
		       b.generated_count, b.redeemed_count, b.valid_from, b.valid_to,
		       b.credit_target, b.created_by, COALESCE(u.email, ''), b.created_at
		  FROM t_redeem_batch b
		  LEFT JOIN t_user u ON u.id = b.created_by
		 ORDER BY `+orderBy+` `+direction+`, b.id `+direction+`
		 LIMIT ? OFFSET ?`, page.Size, page.OffsetInt64())
	if err != nil {
		s.logError(ctx, "fund_redeem_batch_list_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	defer rows.Close()

	items := make([]redeemBatchView, 0, page.Size)
	for rows.Next() {
		var v redeemBatchView
		if err := rows.Scan(&v.ID, &v.BatchName, &v.FaceValueFen, &v.Quantity,
			&v.GeneratedCount, &v.RedeemedCount, &v.ValidFrom, &v.ValidTo,
			&v.CreditTarget, &v.CreatedBy, &v.CreatedByEmail, &v.CreatedAt); err != nil {
			s.logError(ctx, "fund_redeem_batch_scan_failed: "+err.Error())
			mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
			return
		}
		v.FaceValueYuan = v.FaceValueFen / FenPerYuan
		v.ValidFrom = v.ValidFrom.UTC()
		v.ValidTo = v.ValidTo.UTC()
		v.CreatedAt = v.CreatedAt.UTC()
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "fund_redeem_batch_rows_failed: "+err.Error())
		mw.WriteError(w, r, apierr.New(apierr.CodeInternalError))
		return
	}
	_ = mw.WriteJSON(w, http.StatusOK, redeemBatchListResponse{
		Items: items, Page: page.Number, PageSize: page.Size, Total: total,
	})
}
