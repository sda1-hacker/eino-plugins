//go:build darwin || linux || windows

package sandboxfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
)

// walkCleanup 撤销命令新建对象的权限。搜索的条目上限不能用于资源清理：
// 命令可以创建更多对象；单个对象失败也不应阻止其余对象的撤销。
// 分批读取限制单次分配，ctx 限制总时间，汇总错误限制诊断内存。
func (b *Backend) walkCleanup(ctx context.Context, visit func(string) error) error {
	var errs []error
	failures := 0
	record := func(err error) {
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		failures++
		if len(errs) < 16 {
			errs = append(errs, err)
		}
	}
	var walk func(string)
	walk = func(dir string) {
		if ctx.Err() != nil {
			return
		}
		f, err := openRootRead(b.root, dir)
		if err != nil {
			record(err)
			return
		}
		defer f.Close()
		for ctx.Err() == nil {
			entries, readErr := f.ReadDir(128)
			for _, entry := range entries {
				if ctx.Err() != nil {
					return
				}
				if entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				rel := path.Join(dir, entry.Name())
				info, err := b.root.Lstat(rel)
				if err != nil {
					record(err)
					continue
				}
				if !info.IsDir() && !info.Mode().IsRegular() {
					continue
				}
				record(visit(rel))
				// 父目录撤销失败仍尝试后代；成功则先断开继承源，再撤销后代。
				if info.IsDir() {
					walk(rel)
				}
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					record(readErr)
				}
				return
			}
		}
	}
	walk(".")
	if failures > len(errs) {
		errs = append(errs, fmt.Errorf("另有 %d 个权限清理错误", failures-len(errs)))
	}
	return errors.Join(append(errs, ctx.Err())...)
}
