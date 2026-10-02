package tariff

import "time"

// RegisterVersionInput 是登记费率版本的输入。
type RegisterVersionInput struct {
	// Item 费率项标识（必填）。
	Item string
	// ID 同项内唯一的版本标识（必填）。
	ID string
	// UnitPrice 整数分单价，可以为零，不能为负。
	UnitPrice int64
	// Start 生效时间（必填）。
	Start time.Time
	// End 可选的结束时间，须晚于 Start；为空表示持续有效。
	End *time.Time
	// Replaces 可选的被替代版本标识，须为同项内已存在的旧版本。
	Replaces string
}

// VersionView 是版本视图，包含登记信息、替代关系与实际有效区间。
type VersionView struct {
	// ID 版本标识。
	ID string
	// UnitPrice 整数分单价。
	UnitPrice int64
	// RegisteredStart 登记的生效时间。
	RegisteredStart time.Time
	// RegisteredEnd 登记的结束时间，为空表示登记时未填写。
	RegisteredEnd *time.Time
	// Replaces 替代的旧版本标识，为空表示未声明替代。
	Replaces string
	// EffectiveStart 实际有效区间起点，与登记起点一致。
	EffectiveStart time.Time
	// EffectiveEnd 实际有效区间终点；若被替代则为替代者的开始时刻，
	// 为空表示持续有效。
	EffectiveEnd *time.Time
}

// ItemView 是费率项视图。
type ItemView struct {
	// ID 费率项标识。
	ID string
	// Versions 按登记顺序排列的各版本视图。
	Versions []VersionView
}

// RegisterVersion 登记一个费率版本。
//
// 所有校验都在锁内完成，任一校验失败都不会产生或变更任何状态：
// 版本标识非法、单价为负、结束时间不晚于开始时间、同项内版本标识重复、
// 有效区间与同项其他版本重叠、替代约束不满足时整次登记被拒绝。
//
// 替代约束：新版本开始必须晚于被替代版本的开始，且落在被替代版本当前
// 的有效区间内；登记成功后，被替代版本从新版本开始时刻起失效。
func (b *Book) RegisterVersion(in RegisterVersionInput) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if in.Item == "" {
		return reasonError(ReasonInvalidItem, "费率项标识为空")
	}
	if in.ID == "" {
		return reasonError(ReasonInvalidVersion, "版本标识为空")
	}
	if in.UnitPrice < 0 {
		return reasonError(ReasonInvalidPrice, "单价值不能为负")
	}
	if in.End != nil && !in.End.After(in.Start) {
		return reasonError(ReasonInvalidInterval, "结束时间必须晚于生效时间")
	}

	item := b.items[in.Item]
	if item == nil {
		item = &itemState{versions: make(map[string]*versionState)}
	}
	if _, exists := item.versions[in.ID]; exists {
		return reasonError(ReasonVersionExists, "版本标识已存在")
	}

	var old *versionState
	if in.Replaces != "" {
		old = item.versions[in.Replaces]
		if old == nil {
			return reasonError(ReasonReplacesNotFound, "被替代版本不存在或属于另一项")
		}
		if !in.Start.After(old.start) {
			return reasonError(ReasonReplacesOutside, "新版本开始必须晚于被替代版本的开始")
		}
		if effEnd := b.effectiveEnd(old, item); effEnd != nil && !in.Start.Before(*effEnd) {
			return reasonError(ReasonReplacesOutside, "新版本开始必须落在被替代版本当前的有效区间内")
		}
	}

	// 重叠校验：被替代版本会在登记后于新版本开始时刻失效，因此跳过它；
	// 其余版本的实际有效区间都不能与新版本区间重叠。
	for _, v := range item.versions {
		if old != nil && v.id == old.id {
			continue
		}
		if effEnd := b.effectiveEnd(v, item); effEnd != nil && !in.Start.Before(*effEnd) {
			continue
		}
		if in.End != nil && !v.start.Before(*in.End) {
			continue
		}
		return reasonError(ReasonOverlap, "新版本与同项其他版本的有效区间重叠")
	}

	nv := &versionState{
		id:        in.ID,
		unitPrice: in.UnitPrice,
		start:     in.Start,
		end:       cloneTimePtr(in.End),
		replaces:  in.Replaces,
	}
	item.versions[in.ID] = nv
	item.order = append(item.order, in.ID)
	b.items[in.Item] = item
	return nil
}

// GetItem 返回费率项视图；费率项不存在时返回 ReasonItemNotFound。
func (b *Book) GetItem(itemID string) (*ItemView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	item := b.items[itemID]
	if item == nil {
		return nil, reasonError(ReasonItemNotFound, "费率项不存在")
	}
	view := &ItemView{
		ID:       itemID,
		Versions: make([]VersionView, 0, len(item.order)),
	}
	for _, id := range item.order {
		v := item.versions[id]
		view.Versions = append(view.Versions, VersionView{
			ID:              v.id,
			UnitPrice:       v.unitPrice,
			RegisteredStart: v.start,
			RegisteredEnd:   cloneTimePtr(v.end),
			Replaces:        v.replaces,
			EffectiveStart:  v.start,
			EffectiveEnd:    b.effectiveEnd(v, item),
		})
	}
	return view, nil
}
