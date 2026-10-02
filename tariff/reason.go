package tariff

// Reason 是业务拒绝原因的类型。
type Reason string

const (
	// ReasonEmptyRequestID 请求标识为空。
	ReasonEmptyRequestID Reason = "empty_request_id"
	// ReasonInvalidQuantity 数量不是正整数。
	ReasonInvalidQuantity Reason = "invalid_quantity"
	// ReasonItemNotFound 费率项不存在。
	ReasonItemNotFound Reason = "item_not_found"
	// ReasonVersionNotFound 版本不存在。
	ReasonVersionNotFound Reason = "version_not_found"
	// ReasonNotEffective 版本尚未生效。
	ReasonNotEffective Reason = "not_effective"
	// ReasonExpired 版本已失效（含被替代导致的提前失效）。
	ReasonExpired Reason = "expired"
	// ReasonOverflow 总价超过有符号 64 位整数上限。
	ReasonOverflow Reason = "overflow"
	// ReasonRequestConflict 请求标识已用于不同的业务内容。
	ReasonRequestConflict Reason = "request_conflict"
	// ReasonRequestNotFound 请求标识不存在。
	ReasonRequestNotFound Reason = "request_not_found"
	// ReasonInvalidItem 费率项标识为空。
	ReasonInvalidItem Reason = "invalid_item"
	// ReasonInvalidVersion 版本标识为空或已存在。
	ReasonInvalidVersion Reason = "invalid_version"
	// ReasonInvalidPrice 单价值为负。
	ReasonInvalidPrice Reason = "invalid_price"
	// ReasonInvalidInterval 结束时间不晚于生效时间。
	ReasonInvalidInterval Reason = "invalid_interval"
	// ReasonVersionExists 同项内版本标识已存在。
	ReasonVersionExists Reason = "version_exists"
	// ReasonOverlap 有效区间与同项其他版本重叠。
	ReasonOverlap Reason = "overlap"
	// ReasonReplacesNotFound 被替代版本不存在。
	ReasonReplacesNotFound Reason = "replaces_not_found"
	// ReasonReplacesOutside 新版本开始时刻不满足替代约束。
	ReasonReplacesOutside Reason = "replaces_outside"
)

// Error 表示一次可区分原因的业务失败。
type Error struct {
	Reason  Reason
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return string(e.Reason)
}

// IsReason 判断错误是否为指定原因。
func IsReason(err error, reason Reason) bool {
	e, ok := err.(*Error)
	return ok && e.Reason == reason
}

func reasonError(reason Reason, message string) *Error {
	return &Error{Reason: reason, Message: message}
}
