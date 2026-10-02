// Package tariff 是本地费率版本与报价账本。
//
// Book 是账本入口：通过 RegisterVersion 登记费率版本，通过 Quote 按指定
// 版本取得确认价，通过 GetQuote 查询报价使用了什么费率或为何被拒绝。
// 所有方法并发安全。
package tariff

// Ready 表示基线可以运行。
func Ready() bool { return true }
