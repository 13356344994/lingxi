package mechanismpanel

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// JSONLWriter 是一个只追加的小型 JSONL 落盘器,供 cmd/mechanism-panel 把
// EntryFeatures/FutureLabel 写到两个物理上完全独立的文件。
type JSONLWriter struct {
	f *os.File
	w *bufio.Writer
}

// NewJSONLWriter 以追加模式打开(或创建)目标文件。
func NewJSONLWriter(path string) (*JSONLWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("mechanismpanel: open %s: %w", path, err)
	}
	return &JSONLWriter{f: f, w: bufio.NewWriter(f)}, nil
}

// Write 编码一条记录并写一行,末尾自带换行。
func (w *JSONLWriter) Write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.w.Write(b); err != nil {
		return err
	}
	if err := w.w.WriteByte('\n'); err != nil {
		return err
	}
	// Pipeline may checkpoint immediately after the callback returns. Flush here so a
	// checkpoint can never confirm an output line that still exists only in user-space buffers.
	return w.w.Flush()
}

// Close 落盘缓冲区并关闭文件。
func (w *JSONLWriter) Close() error {
	if err := w.w.Flush(); err != nil {
		w.f.Close()
		return err
	}
	return w.f.Close()
}

// NewJSONLWriterResumeAt 以续写模式打开 path,但先把文件截断到刚好 keepLines 行,再从
// 那个位置开始追加(红队#12:崩溃恢复/context 取消不得重复 append)。
//
// 动机:Pipeline 的 checkpoint 只在主循环整体走完一次之后落盘一次,而 onFeature/onLabel
// 每算出一条记录就会立即调用一次 Write。如果进程在"已经写出第 N+1、N+2 行"之后、但在
// 下一次 checkpoint 落盘之前崩溃或被杀,checkpoint 里记录的 FeaturesEmitted/LabelsEmitted
// 仍然是 N,而输出文件里已经多出了 N+1、N+2 两行"没有被 checkpoint 确认过"的尾巴。下次
// resume 会重新计算并再次调用 onFeature 产出等价于 N+1、N+2 的记录——如果直接以普通追加
// 模式打开,这两行就会被重复 append 一遍。NewJSONLWriterResumeAt 用 checkpoint 里的
// FeaturesEmitted/LabelsEmitted 作为 keepLines,先剪掉这条未被确认的尾巴,让 resume 之后
// 的输出行数与 checkpoint 记录的计数重新对齐。
//
// 只扫描到第 keepLines 行为止就停止,不读文件里剩下的内容,对不断增长的大输出文件也是
// bounded 操作。文件实际行数 <= keepLines 时视为"已经不需要剪",直接以追加模式打开。
func NewJSONLWriterResumeAt(path string, keepLines int64) (*JSONLWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("mechanismpanel: open %s: %w", path, err)
	}
	cut, err := jsonlTruncationOffset(f, keepLines)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("mechanismpanel: scan %s for resume cut point: %w", path, err)
	}
	if err := f.Truncate(cut); err != nil {
		f.Close()
		return nil, fmt.Errorf("mechanismpanel: truncate %s to %d bytes: %w", path, cut, err)
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, fmt.Errorf("mechanismpanel: seek %s: %w", path, err)
	}
	return &JSONLWriter{f: f, w: bufio.NewWriter(f)}, nil
}

// jsonlTruncationOffset 从头扫描 f 的前 keepLines 个完整行(以 '\n' 结尾),返回第
// keepLines 行结束处(含换行符)的字节偏移,供调用方 Truncate 到这个位置——之后任何
// 多出来的内容(上次崩溃/取消前写出的、未被 checkpoint 确认过的尾巴,不管是完整行还是
// 写到一半的半行)都会被剪掉。
//
// 如果文件里连 keepLines 行都凑不齐(哪怕最后一行是写到一半没有换行符的半行),说明输出
// 文件与 checkpoint 已经不同步(比如指错了输出文件路径、或输出文件本身被截断/损坏),这是
// 一个需要 fail closed 的错误,不能悄悄当作"从头开始"处理。
func jsonlTruncationOffset(f *os.File, keepLines int64) (int64, error) {
	if keepLines < 0 {
		keepLines = 0
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	r := bufio.NewReader(f)
	var offset int64
	for i := int64(0); i < keepLines; i++ {
		line, err := r.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return 0, fmt.Errorf("only %d of %d confirmed line(s) present (%q...); output file is out of sync with checkpoint", i, keepLines, truncateForError(line))
			}
			return 0, err
		}
		offset += int64(len(line))
	}
	return offset, nil
}

func truncateForError(b []byte) string {
	const max = 40
	if len(b) > max {
		b = b[:max]
	}
	return string(b)
}
