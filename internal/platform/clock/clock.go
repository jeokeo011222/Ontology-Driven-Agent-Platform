// Package clock 抽象时间来源，保证状态机、租约与测试可确定性复现。
package clock

import "time"

// Clock 提供可替换的时间源。
type Clock interface {
	Now() time.Time
}

// RealClock 是生产时钟。
type RealClock struct{}

// Now 返回当前时间。
func (RealClock) Now() time.Time { return time.Now().UTC() }

// FakeClock 是测试用可控时钟。
type FakeClock struct {
	Current time.Time
}

// Now 返回 FakeClock 的当前设定时间。
func (c *FakeClock) Now() time.Time { return c.Current }

// Advance 推进假时钟。
func (c *FakeClock) Advance(d time.Duration) { c.Current = c.Current.Add(d) }

// Set 设定假时钟。
func (c *FakeClock) Set(t time.Time) { c.Current = t }
