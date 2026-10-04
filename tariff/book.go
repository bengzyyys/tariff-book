package tariff

import (
	"errors"
	"math"
	"sort"
	"sync"
	"time"
)

// 登记费率版本时可能返回的错误。
var (
	ErrEmptyItemID            = errors.New("tariff: item id must not be empty")
	ErrEmptyVersionID         = errors.New("tariff: version id must not be empty")
	ErrInvalidUnitPrice       = errors.New("tariff: unit price must not be negative")
	ErrStartRequired          = errors.New("tariff: effective start time is required")
	ErrInvalidInterval        = errors.New("tariff: end time must be after start time")
	ErrVersionExists          = errors.New("tariff: version id already registered for the item")
	ErrOverlap                = errors.New("tariff: version interval overlaps an existing version of the item")
	ErrReplaceTargetNotFound  = errors.New("tariff: replaced version does not exist")
	ErrReplaceTargetWrongItem = errors.New("tariff: replaced version belongs to another item")
	ErrInvalidReplacement     = errors.New("tariff: new version start must be after the replaced version start and within its current effective interval")
)

// 查询与报价时可能返回的错误。
var (
	ErrItemNotFound       = errors.New("tariff: rate item not found")
	ErrNoEffectiveVersion = errors.New("tariff: no effective version for the rate item at the given time")
	ErrRequestNotFound    = errors.New("tariff: request id not found")
	ErrRequestIDConflict  = errors.New("tariff: request id already used with different content")
)

// RejectReason 区分报价被拒绝的原因。
type RejectReason string

const (
	ReasonNone                   RejectReason = ""
	ReasonEmptyRequestID         RejectReason = "empty_request_id"
	ReasonInvalidQuantity        RejectReason = "invalid_quantity"
	ReasonVersionNotFound        RejectReason = "version_not_found"
	ReasonVersionNotYetEffective RejectReason = "version_not_yet_effective"
	ReasonVersionExpired         RejectReason = "version_expired"
	ReasonTotalOverflow          RejectReason = "total_overflow"
)

// RegisterRequest 描述一次费率版本登记。
type RegisterRequest struct {
	ItemID    string
	VersionID string
	UnitPrice int64      // 整数分单价，可以为零，不能为负
	Start     time.Time  // 必填，生效时刻（含）
	End       *time.Time // 可选，结束时刻（不含）；nil 表示持续有效
	Replaces  string     // 可选，同项内被本版本替代的旧版本标识
}

// VersionView 是查询费率项时返回的版本视图，
// 同时包含登记的起止时间、替代关系和实际有效区间。
type VersionView struct {
	ItemID         string
	VersionID      string
	UnitPrice      int64
	Start          time.Time  // 登记的生效时刻（含）
	End            *time.Time // 登记的结束时刻（不含），nil 表示持续有效
	Replaces       string     // 本版本登记时替代的旧版本，空表示无
	SupersededBy   string     // 替代本版本的新版本，空表示未被替代
	EffectiveStart time.Time  // 实际有效区间起点（含）
	EffectiveEnd   *time.Time // 实际有效区间终点（不含），nil 表示持续有效
}

// QuoteRequest 描述一次报价请求。
type QuoteRequest struct {
	RequestID string // 非空请求标识，用于幂等与查询
	ItemID    string
	VersionID string
	Quantity  int64 // 正整数
}

// Outcome 是报价的首次受理结果：要么确认，要么带原因拒绝。
// 成功报价即为已确认记录，之后费率变化不会改写其中的金额和来源。
type Outcome struct {
	Request    QuoteRequest // 原请求内容
	AcceptedAt time.Time    // 首次受理时刻
	Confirmed  bool
	UnitPrice  int64        // 确认时的实际单价（分）
	Total      int64        // 确认时的总价（分）
	Reason     RejectReason // 被拒绝时的原因
}

// version 是登记在册的费率版本。effEnd 因被替代而可能早于登记的 end。
type version struct {
	itemID       string
	versionID    string
	unitPrice    int64
	start        time.Time
	end          *time.Time
	replaces     string
	supersededBy string
	effEnd       *time.Time
}

// Book 是费率版本与报价的本地账本，可并发使用。
type Book struct {
	mu       sync.Mutex
	now      func() time.Time
	items    map[string]map[string]*version // itemID -> versionID -> version
	requests map[string]Outcome             // requestID -> 首次受理结果
}

// Option 定制 Book 的行为。
type Option func(*Book)

// WithClock 指定受理时刻的来源，默认使用 time.Now。
func WithClock(now func() time.Time) Option {
	return func(b *Book) {
		if now != nil {
			b.now = now
		}
	}
}

// NewBook 返回一个空的账本。
func NewBook(opts ...Option) *Book {
	b := &Book{
		now:      time.Now,
		items:    make(map[string]map[string]*version),
		requests: make(map[string]Outcome),
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// RegisterVersion 登记一个费率版本。任何校验失败都不会留下部分变更。
func (b *Book) RegisterVersion(req RegisterRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if req.ItemID == "" {
		return ErrEmptyItemID
	}
	if req.VersionID == "" {
		return ErrEmptyVersionID
	}
	if req.UnitPrice < 0 {
		return ErrInvalidUnitPrice
	}
	if req.Start.IsZero() {
		return ErrStartRequired
	}
	if req.End != nil && !req.End.After(req.Start) {
		return ErrInvalidInterval
	}

	versions := b.items[req.ItemID]
	if _, ok := versions[req.VersionID]; ok {
		return ErrVersionExists
	}

	var target *version
	if req.Replaces != "" {
		t, ok := versions[req.Replaces]
		if !ok {
			if b.versionInOtherItem(req.ItemID, req.Replaces) {
				return ErrReplaceTargetWrongItem
			}
			return ErrReplaceTargetNotFound
		}
		// 新版本的开始必须晚于旧版本的开始，且落在旧版本当前的有效区间内。
		if !req.Start.After(t.start) {
			return ErrInvalidReplacement
		}
		if t.effEnd != nil && !req.Start.Before(*t.effEnd) {
			return ErrInvalidReplacement
		}
		target = t
	}

	// 与同项其他版本的实际有效区间比较，不允许重叠；
	// 被替代版本会被截断到新版本开始时刻，不参与重叠判断。
	for _, v := range versions {
		if v == target {
			continue
		}
		if intervalsOverlap(req.Start, req.End, v.start, v.effEnd) {
			return ErrOverlap
		}
	}

	// 全部校验通过后才提交变更。结束时刻必须复制后入库：
	// 既不能与调用方的请求变量共享指针，登记结束与实际结束之间也互为独立副本，
	// 外部对原变量或任一返回结果的修改都不能改写账本中的边界。
	v := &version{
		itemID:    req.ItemID,
		versionID: req.VersionID,
		unitPrice: req.UnitPrice,
		start:     req.Start,
		end:       cloneTimePtr(req.End),
		replaces:  req.Replaces,
		effEnd:    cloneTimePtr(req.End),
	}
	if target != nil {
		truncated := req.Start
		target.effEnd = &truncated
		target.supersededBy = req.VersionID
	}
	if versions == nil {
		versions = make(map[string]*version)
		b.items[req.ItemID] = versions
	}
	versions[req.VersionID] = v
	return nil
}

func (b *Book) versionInOtherItem(itemID, versionID string) bool {
	for id, versions := range b.items {
		if id == itemID {
			continue
		}
		if _, ok := versions[versionID]; ok {
			return true
		}
	}
	return false
}

// cloneTimePtr 返回结束时刻的独立副本，nil 原样返回。
// time.Time 是值类型，但账本持有的是 *time.Time，必须复制指针指向的值，
// 否则调用方修改自己的时间变量就会改写账本。
func cloneTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}

// intervalsOverlap 判断半开区间 [s1,e1) 与 [s2,e2) 是否重叠，nil 终点表示持续有效。
// 时间比较按实际时刻进行，不同时区偏移不影响结果。
func intervalsOverlap(s1 time.Time, e1 *time.Time, s2 time.Time, e2 *time.Time) bool {
	if e1 != nil && !s2.Before(*e1) {
		return false
	}
	if e2 != nil && !s1.Before(*e2) {
		return false
	}
	return true
}

// versionView 构造账本版本的独立视图。
// 结束时刻必须复制后返回：登记结束与实际结束各自生成独立副本，
// 不同次查询之间也不共享指针，外部对任一返回结果的修改都不能改写账本边界。
func versionView(v *version) VersionView {
	return VersionView{
		ItemID:         v.itemID,
		VersionID:      v.versionID,
		UnitPrice:      v.unitPrice,
		Start:          v.start,
		End:            cloneTimePtr(v.end),
		Replaces:       v.replaces,
		SupersededBy:   v.supersededBy,
		EffectiveStart: v.start,
		EffectiveEnd:   cloneTimePtr(v.effEnd),
	}
}

// ItemVersions 返回费率项下各版本的登记信息与实际有效区间，按生效时刻排序。
func (b *Book) ItemVersions(itemID string) ([]VersionView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	versions, ok := b.items[itemID]
	if !ok || len(versions) == 0 {
		return nil, ErrItemNotFound
	}
	views := make([]VersionView, 0, len(versions))
	for _, v := range versions {
		views = append(views, versionView(v))
	}
	sort.Slice(views, func(i, j int) bool {
		if !views[i].EffectiveStart.Equal(views[j].EffectiveStart) {
			return views[i].EffectiveStart.Before(views[j].EffectiveStart)
		}
		return views[i].VersionID < views[j].VersionID
	})
	return views, nil
}

// EffectiveVersionAt 返回指定费率项在 at 这一瞬间实际生效的单个版本。
//
// 选择依据是查询时账本已经登记的实际有效区间 [EffectiveStart, EffectiveEnd)：
// 开始时刻包含在内，结束时刻不包含在内，实际结束为 nil 表示持续有效。
// 旧版本被替代后，其实际有效区间止于交接时刻，从交接时刻起便不会再被选中，
// 即便它登记的结束时间仍在将来；替代它的新版本到期后也不会回退到旧版本。
// at 可以是过去或未来的任意瞬间，与账本时钟无关。
//
// 时间按实际时刻比较，同一瞬间的不同时区表示结论一致；
// 选择只在指定费率项内进行，其他费率项中同名的版本不参与。
//
// 费率项从未登记过版本时返回 ErrItemNotFound；费率项存在但该瞬间没有任何
// 生效版本（早于首版开始、落在两版之间的空档或全部已到期）时返回
// ErrNoEffectiveVersion，不会拿邻近版本补位。
//
// 该查询只读取版本信息，不受理报价、不占用请求标识，也不影响已保存的
// 受理结果。返回的 VersionView 是独立副本，修改其中的结束时间不会改变账本。
func (b *Book) EffectiveVersionAt(itemID string, at time.Time) (VersionView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	versions, ok := b.items[itemID]
	if !ok || len(versions) == 0 {
		return VersionView{}, ErrItemNotFound
	}
	var found *version
	for _, v := range versions {
		if at.Before(v.start) {
			continue
		}
		if v.effEnd != nil && !at.Before(*v.effEnd) {
			continue
		}
		// 同一费率项内各版本的实际有效区间互不重叠，至多一版命中。
		found = v
		break
	}
	if found == nil {
		return VersionView{}, ErrNoEffectiveVersion
	}
	return versionView(found), nil
}

// Quote 按首次受理时刻判断指定版本是否有效并给出报价。
// 非空请求标识的首次结果（无论确认还是拒绝）都会被保存；
// 相同标识且业务内容相同的重试返回首次结果，内容不同则返回 ErrRequestIDConflict。
func (b *Book) Quote(req QuoteRequest) (Outcome, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if req.RequestID == "" {
		return Outcome{
			Request:    req,
			AcceptedAt: b.now(),
			Reason:     ReasonEmptyRequestID,
		}, nil
	}

	if prev, ok := b.requests[req.RequestID]; ok {
		same := prev.Request.ItemID == req.ItemID &&
			prev.Request.VersionID == req.VersionID &&
			prev.Request.Quantity == req.Quantity
		if !same {
			return Outcome{}, ErrRequestIDConflict
		}
		return prev, nil
	}

	out := Outcome{Request: req, AcceptedAt: b.now()}
	switch {
	case req.Quantity <= 0:
		out.Reason = ReasonInvalidQuantity
	default:
		v := b.items[req.ItemID][req.VersionID]
		switch {
		case v == nil:
			out.Reason = ReasonVersionNotFound
		case out.AcceptedAt.Before(v.start):
			out.Reason = ReasonVersionNotYetEffective
		case v.effEnd != nil && !out.AcceptedAt.Before(*v.effEnd):
			out.Reason = ReasonVersionExpired
		case v.unitPrice > 0 && req.Quantity > math.MaxInt64/v.unitPrice:
			out.Reason = ReasonTotalOverflow
		default:
			out.Confirmed = true
			out.UnitPrice = v.unitPrice
			out.Total = v.unitPrice * req.Quantity
		}
	}
	b.requests[req.RequestID] = out
	return out, nil
}

// Lookup 按请求标识查询保存的首次受理结果，未见过的标识返回 ErrRequestNotFound。
func (b *Book) Lookup(requestID string) (Outcome, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	out, ok := b.requests[requestID]
	if !ok {
		return Outcome{}, ErrRequestNotFound
	}
	return out, nil
}
