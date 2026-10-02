package tariff

import (
	"math"
	"time"
)

// QuoteRequest 是报价请求。
type QuoteRequest struct {
	// RequestID 请求标识。非空时，首次请求的结果会被保存，
	// 后续相同标识且业务内容相同的重试直接返回首次结果。
	RequestID string
	// Item 费率项标识。
	Item string
	// Version 版本标识，按首次受理时刻判定其有效性，不会自动换用其他版本。
	Version string
	// Quantity 正整数数量。
	Quantity int64
}

// ConfirmedQuote 是已确认的报价记录。
//
// 记录保留请求内容、受理时刻、引用版本、实际单价与总价；
// 即使引用版本后来到期或被替代，仍可按请求标识查到原价。
type ConfirmedQuote struct {
	RequestID  string
	Item       string
	Version    string
	Quantity   int64
	UnitPrice  int64
	TotalPrice int64
	AcceptedAt time.Time
}

// Rejection 是被拒绝的报价及原因。
//
// 保存的拒绝记录会回显原请求内容、受理时刻与拒绝原因，
// 以便按标识查询时看到首次请求的完整信息。
type Rejection struct {
	Reason     Reason
	Message    string
	AcceptedAt time.Time
	Item       string
	Version    string
	Quantity   int64
}

// QuoteResult 是报价结果：确认或拒绝，二者恰有一个非空。
type QuoteResult struct {
	RequestID string
	Confirmed *ConfirmedQuote
	Rejected  *Rejection
}

// Quote 处理报价请求。
//
// 有效性按首次受理时刻判定：请求标识为空、数量非正、费率项或版本不存在、
// 版本尚未生效或已经失效、总价超过 int64 上限时拒绝，原因可区分。
//
// 以非空请求标识提交的首个请求无论成败都会保存；相同标识且业务内容相同
// 的重试返回首次结果，不再重新计算；沿用标识却改变费率项、版本或数量时
// 返回 ReasonRequestConflict，且不覆盖首次记录。
func (b *Book) Quote(req QuoteRequest) (*QuoteResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	result := b.evaluate(req, b.clock())

	if req.RequestID == "" {
		return cloneResult(result), nil
	}

	if existing, ok := b.requests[req.RequestID]; ok {
		if !sameBusiness(existing.request, req) {
			return nil, reasonError(ReasonRequestConflict, "请求标识已用于不同的业务内容")
		}
		return cloneResult(existing.result), nil
	}

	b.requests[req.RequestID] = &requestRecord{
		request: req,
		result:  cloneResult(result),
	}
	return cloneResult(result), nil
}

// GetQuote 按请求标识返回已保存的报价结果。
// 未见过的标识返回 ReasonRequestNotFound。
func (b *Book) GetQuote(requestID string) (*QuoteResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	rec, ok := b.requests[requestID]
	if !ok {
		return nil, reasonError(ReasonRequestNotFound, "请求标识不存在")
	}
	return cloneResult(rec.result), nil
}

// evaluate 执行实际的报价判定与计价。调用方必须持有 b.mu。
func (b *Book) evaluate(req QuoteRequest, now time.Time) *QuoteResult {
	res := &QuoteResult{RequestID: req.RequestID}
	reject := func(reason Reason, message string) *QuoteResult {
		res.Rejected = &Rejection{
			Reason:     reason,
			Message:    message,
			AcceptedAt: now,
			Item:       req.Item,
			Version:    req.Version,
			Quantity:   req.Quantity,
		}
		return res
	}

	if req.RequestID == "" {
		return reject(ReasonEmptyRequestID, "请求标识为空")
	}
	if req.Quantity <= 0 {
		return reject(ReasonInvalidQuantity, "数量必须为正整数")
	}
	item := b.items[req.Item]
	if item == nil {
		return reject(ReasonItemNotFound, "费率项不存在")
	}
	v := item.versions[req.Version]
	if v == nil {
		return reject(ReasonVersionNotFound, "版本不存在")
	}
	if now.Before(v.start) {
		return reject(ReasonNotEffective, "版本尚未生效")
	}
	if effEnd := b.effectiveEnd(v, item); effEnd != nil && !now.Before(*effEnd) {
		return reject(ReasonExpired, "版本已失效")
	}
	if v.unitPrice > 0 && req.Quantity > math.MaxInt64/v.unitPrice {
		return reject(ReasonOverflow, "总价超过有符号 64 位整数上限")
	}

	res.Confirmed = &ConfirmedQuote{
		RequestID:  req.RequestID,
		Item:       req.Item,
		Version:    req.Version,
		Quantity:   req.Quantity,
		UnitPrice:  v.unitPrice,
		TotalPrice: v.unitPrice * req.Quantity,
		AcceptedAt: now,
	}
	return res
}

func sameBusiness(a, b QuoteRequest) bool {
	return a.Item == b.Item && a.Version == b.Version && a.Quantity == b.Quantity
}

func cloneResult(r *QuoteResult) *QuoteResult {
	if r == nil {
		return nil
	}
	c := *r
	if r.Confirmed != nil {
		cc := *r.Confirmed
		c.Confirmed = &cc
	}
	if r.Rejected != nil {
		cr := *r.Rejected
		c.Rejected = &cr
	}
	return &c
}
