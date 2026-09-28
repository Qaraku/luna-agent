package store

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
)

// scanRecords 逐行校验并交付记录。重用长行缓冲，不为每次读取保留整份日志；
// 不使用带默认 64 KiB token 上限的 Scanner，以免拒绝已有的大工具结果。
func scanRecords(input io.Reader, path string, visit func(Record)) (bool, error) {
	reader := bufio.NewReader(input)
	var pending []byte
	for lineNumber := 1; ; {
		fragment, err := reader.ReadSlice('\n')
		if len(pending) > 0 {
			pending = append(pending, fragment...)
			fragment = pending
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			if len(pending) == 0 {
				pending = append(pending, fragment...)
			}
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				// 没有结束换行的尾片段一律不解码，包括本身恰好是合法 JSON 的情况。
				return len(fragment) > 0, nil
			}
			return false, fmt.Errorf("store: read session file: %w", err)
		}
		if len(bytes.TrimSpace(fragment)) != 0 {
			// 与旧的 Split("\n") 一致：分隔符不参与 JSON 解码，也不能改变损坏行的错误文本。
			record, err := decodeLine(fragment[:len(fragment)-1])
			if err != nil {
				return false, fmt.Errorf("%w %s line %d: %v", ErrCorrupt, filepath.Base(path), lineNumber, err)
			}
			visit(record)
		}
		pending = pending[:0]
		lineNumber++
	}
}
