package tariff

import (
	"sync"
	"time"
)

// Book 是本地费率版本与报价的内存账本。
//
// Book 的所有方法都是并发安全的：登记、报价与查询共用同一把互斥锁，
// 因此并发提交的同一请求标识只会留下第一条记录。
type Book struct {
	mu       sync.Mutex
	clock    func() time.Time
	items    map[string]*itemState
	requests map[string]*requestRecord
}

// Option 用于配置 Book。
type Option func(*Book)

// WithClock 指定受理时刻的取值来源，默认使用 time.Now。
// 主要用于测试：业务代码不应注入会回拨的时钟。
func WithClock(clock func() time.Time) Option {
	return func(b *Book) { b.clock = clock }
}

// New 创建账本。
func New(opts ...Option) *Book {
	b := &Book{
		clock:    time.Now,
		items:    make(map[string]*itemState),
		requests: make(map[string]*requestRecord),
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// itemState 是一个费率项的内部状态。
type itemState struct {
	versions map[string]*versionState
	order    []string // 登记顺序
}

// versionState 是一个费率版本的内部状态。
type versionState struct {
	id        string
	unitPrice int64
	start     time.Time
	end       *time.Time
	replaces  string
}

// requestRecord 是一条请求记录。
type requestRecord struct {
	request QuoteRequest
	result  *QuoteResult
}

// effectiveEnd 返回版本实际的有效区间终点。
//
// 版本自身登记的终点为基准；若有版本声明替代它，则以替代者的开始时刻
// 作为其失效时刻（多个替代者时取最早的开始时刻）。返回 nil 表示持续有效。
// 调用方必须持有 b.mu。
func (b *Book) effectiveEnd(v *versionState, item *itemState) *time.Time {
	end := cloneTimePtr(v.end)
	for _, r := range item.versions {
		if r.replaces != v.id {
			continue
		}
		if end == nil || r.start.Before(*end) {
			t := r.start
			end = &t
		}
	}
	return end
}

func cloneTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}
