// Package preempt 提供抢占受害者选择的纯函数(损失最小口径,D3 定稿)。
package preempt

import "sort"

// VictimCandidate 一个候选受害者。
// 排序规则(损失最小优先被抢):
//  1. 优先级升序 —— 低优先级语义弱,先被抢
//  2. 有 checkpoint 优先 —— 有快照能恢复,杀了可重来
//  3. 剩余运行时间长优先 —— 刚起步投入少,损失最小;剩余短 = 进度深 = 投入多 = 功亏一篑,最后抢
type VictimCandidate struct {
	PodKey           string
	Priority         int32   // k8s 语义:值越小优先级越低
	HasCheckpoint    bool
	RemainingRuntime float64 // 单位:秒,正数
}

// SortVictims 按"损失最小优先被抢"排序,返回新切片(不改入参)。
func SortVictims(candidates []VictimCandidate) []VictimCandidate {
	sorted := make([]VictimCandidate, len(candidates))
	copy(sorted, candidates)
	sort.SliceStable(sorted, func(i, j int) bool {
		return LessVictim(sorted[i], sorted[j])
	})
	return sorted
}

// LessVictim 比较两候选:返回 true 表示 a 应先被抢(损失更小)。
func LessVictim(a, b VictimCandidate) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if a.HasCheckpoint != b.HasCheckpoint {
		return a.HasCheckpoint
	}
	return a.RemainingRuntime > b.RemainingRuntime
}
