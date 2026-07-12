package mechanismpanel

import (
	"container/heap"
	"fmt"
)

// Merger 是多路 k-way 归并:任意时刻每个 Source 只缓冲一条已 peek 的记录,
// 内存占用与 Source 数量成正比、与文件大小无关(bounded memory 的核心保证)。
//
// 注意 checkpoint 续读位置必须用 EmittedCounts,不能用 Source.LinesRead:归并器为了
// peek 下一条记录,任何时刻每个未耗尽的源都天然多读了一行"预取但尚未吐出"的记录;
// 若照 LinesRead 落盘偏移量,resume 会把这行已预取却从未真正处理过的记录跳过丢失。
type Merger struct {
	h        *mergeHeap
	emitted  map[string]int64
}

type mergeItem struct {
	src Source
	rec Record
}

type mergeHeap []*mergeItem

func (h mergeHeap) Len() int { return len(h) }
func (h mergeHeap) Less(i, j int) bool {
	ti, tj := h[i].rec.EventTime(), h[j].rec.EventTime()
	if !ti.Equal(tj) {
		return ti.Before(tj)
	}
	if h[i].rec.Kind != h[j].rec.Kind {
		return h[i].rec.Kind < h[j].rec.Kind
	}
	return h[i].src.Name() < h[j].src.Name()
}
func (h mergeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *mergeHeap) Push(x any)        { *h = append(*h, x.(*mergeItem)) }
func (h *mergeHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}

// NewMerger 用给定的 sources 初始化归并器,预取每个源的第一条记录。
func NewMerger(sources []Source) (*Merger, error) {
	h := &mergeHeap{}
	heap.Init(h)
	m := &Merger{h: h, emitted: map[string]int64{}}
	for _, src := range sources {
		m.emitted[src.Name()] = 0
		rec, ok, err := src.Next()
		if err != nil {
			return nil, fmt.Errorf("mechanismpanel: prime source %s: %w", src.Name(), err)
		}
		if !ok {
			continue // 空源,直接跳过
		}
		heap.Push(h, &mergeItem{src: src, rec: rec})
	}
	return m, nil
}

// Next 返回全局按 event-time 排序的下一条记录;ok=false 且 err=nil 表示所有源都已耗尽。
func (m *Merger) Next() (Record, bool, error) {
	if m.h.Len() == 0 {
		return Record{}, false, nil
	}
	item := heap.Pop(m.h).(*mergeItem)
	rec := item.rec
	m.emitted[item.src.Name()]++
	next, ok, err := item.src.Next()
	if err != nil {
		return Record{}, false, fmt.Errorf("mechanismpanel: read source %s: %w", item.src.Name(), err)
	}
	if ok {
		heap.Push(m.h, &mergeItem{src: item.src, rec: next})
	}
	return rec, true, nil
}

// EmittedCounts 返回每个源(按 Name())已经被 Next() 真正吐出(而非仅预取)的记录数,
// 这才是 checkpoint 应该落盘的续读行号。
func (m *Merger) EmittedCounts() map[string]int64 {
	out := make(map[string]int64, len(m.emitted))
	for k, v := range m.emitted {
		out[k] = v
	}
	return out
}
