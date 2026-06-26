package proxy

import (
	"sync"
	"time"

	"kiro-go/logger"
	"kiro-go/pool"
)

// 自适应用量刷新调度
//
// 账户用量（UsageCurrent/UsageLimit）只在刷新时从上游拉取，两次刷新之间是冻结值。
// 固定 30 分钟刷新对高频燃烧的账号太慢：可能上次刷新还在 90%，30 分钟内已冲过
// 95% 拦截线，但本地仍显示 90% 而继续被选中，最终撞线失败。
//
// 调度器按「距离 95% 阈值的预计到达时间(ETA)」自适应刷新：
//
//	threshold = limit * QuotaBlockRatio
//	remaining = threshold - current              // 离阈值还差多少额度
//	burnRate  = (current - prev) / (now - prevAt) // 每秒燃烧的额度（权威值差分）
//	eta       = remaining / burnRate              // 还有几秒撞线
//	interval  = clamp(eta * safetyFactor, min, max)
//
// 越接近阈值 remaining 越小、燃烧越快 burnRate 越大，eta 都更小 → 刷得更勤。
// 空闲账号 burnRate≈0 → eta 趋于无穷 → 落到 max（即原 30 分钟，行为不变）。
//
// burnRate 用相邻两次 UsageCurrent 的差分（均为上游权威值），不依赖实时 metering
// credits，从而绕开 credits 单位与 UsageCurrent 单位是否一致的不确定性。代价是
// burnRate 有一个刷新周期的滞后，但 safetyFactor 的余量足以吸收。
const (
	refreshScanInterval = 30 * time.Second // 后台扫描节奏：每轮只刷到期账号
	refreshMinInterval  = 60 * time.Second // 刷新间隔下限，避免打爆上游 getUsageLimits
	refreshMaxInterval  = 30 * time.Minute // 刷新间隔上限，等于改造前的固定节奏
	refreshSafetyFactor = 0.5              // 在预计撞线前留一半余量刷新
	nearThresholdRatio  = 0.8              // 用量达此比例即升级日志为 info，提前预警即将下线
)

// computeRefreshInterval 根据用量快照差分计算下次刷新间隔。
//
// 所有无法估算燃烧速率的情况都回退到 refreshMaxInterval（即现状行为）：
//   - limit<=0：无配额信息
//   - !hasPrev：无历史快照（新账号首刷后的第一轮）
//   - elapsed<=0：时间未推进（异常/同一时刻）
//   - delta<=0：用量未增长（空闲）或下降（配额已重置）
//   - remaining<=0：已达/超过 95% 阈值（已被拦截，慢速轮询探测重置即可）
func computeRefreshInterval(prevCurrent, current, limit float64, elapsed time.Duration, hasPrev bool) time.Duration {
	if limit <= 0 || !hasPrev || elapsed <= 0 {
		return refreshMaxInterval
	}

	threshold := limit * pool.QuotaBlockRatio
	remaining := threshold - current
	if remaining <= 0 {
		return refreshMaxInterval
	}

	delta := current - prevCurrent
	if delta <= 0 {
		return refreshMaxInterval
	}

	burnRate := delta / elapsed.Seconds() // 额度/秒
	etaSeconds := remaining / burnRate
	interval := time.Duration(etaSeconds * refreshSafetyFactor * float64(time.Second))

	if interval < refreshMinInterval {
		return refreshMinInterval
	}
	if interval > refreshMaxInterval {
		return refreshMaxInterval
	}
	return interval
}

// refreshState 记录单个账号的用量快照与下次刷新时间。
type refreshState struct {
	prevCurrent float64   // 上次刷新时的 UsageCurrent
	prevAt      time.Time // 上次刷新的时间
	hasPrev     bool      // 是否已有快照（首刷前为 false）
	nextDue     time.Time // 下次应刷新的时间
}

// refreshScheduler 维护各账号的自适应刷新节奏。运行态派生数据，不持久化，
// 重启后所有账号从 max 间隔重新收敛。并发安全。
type refreshScheduler struct {
	mu     sync.Mutex
	states map[string]*refreshState
}

func newRefreshScheduler() *refreshScheduler {
	return &refreshScheduler{states: make(map[string]*refreshState)}
}

// Due 报告账号是否到期需要刷新。无记录（新账号）视为到期。
func (s *refreshScheduler) Due(id string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[id]
	if !ok {
		return true
	}
	return !now.Before(st.nextDue)
}

// Record 在一次成功刷新后更新快照并重排下次刷新时间。
func (s *refreshScheduler) Record(id string, current, limit float64, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[id]
	if st == nil {
		st = &refreshState{}
		s.states[id] = st
	}

	var interval time.Duration
	var burnRate float64 // 额度/秒，仅用于日志
	if st.hasPrev {
		elapsed := now.Sub(st.prevAt)
		interval = computeRefreshInterval(st.prevCurrent, current, limit, elapsed, true)
		if elapsed > 0 {
			burnRate = (current - st.prevCurrent) / elapsed.Seconds()
		}
	} else {
		interval = refreshMaxInterval
	}

	st.prevCurrent = current
	st.prevAt = now
	st.hasPrev = true
	st.nextDue = now.Add(interval)

	// 接近阈值时升级为 info，方便运维观察账号即将下线；否则 debug。
	pct := 0.0
	if limit > 0 {
		pct = current / limit * 100
	}
	if limit > 0 && current >= limit*nearThresholdRatio {
		logger.Infof("[RefreshSched] %s 接近配额阈值 %.1f/%.1f (%.1f%%, 阈值 %.0f%%) burn=%.4f/s 下次刷新 %v 后",
			id, current, limit, pct, pool.QuotaBlockRatio*100, burnRate, interval)
	} else {
		logger.Debugf("[RefreshSched] %s 用量 %.1f/%.1f (%.1f%%) burn=%.4f/s 下次刷新 %v 后",
			id, current, limit, pct, burnRate, interval)
	}
}

// Backoff 在刷新失败后把下次刷新推迟一个 max 间隔，不动快照，
// 避免坏账号每轮扫描都被重试。
func (s *refreshScheduler) Backoff(id string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[id]
	if st == nil {
		st = &refreshState{}
		s.states[id] = st
	}
	st.nextDue = now.Add(refreshMaxInterval)
}

// Retain 清除不在 validIDs 中的账号状态（账号被删除后回收内存）。
func (s *refreshScheduler) Retain(validIDs map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.states {
		if !validIDs[id] {
			delete(s.states, id)
		}
	}
}
