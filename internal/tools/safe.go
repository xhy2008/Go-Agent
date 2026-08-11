package tools

import (
	"errors"
	"path/filepath"
	"strings"
)

var (
	errInvalidPath = errors.New("路径不能为空")
	errBlockedPath = errors.New("路径位于系统关键目录，已拒绝访问")
)

// safePath 校验路径是否允许访问：拒绝系统关键目录，允许相对路径。
// 返回规范化后的绝对路径。
func safePath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errInvalidPath
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if isBlockedPath(abs) {
		return "", errBlockedPath
	}
	return abs, nil
}

// isBlockedPath 判断路径是否落入系统关键目录（Windows/Linux 通用）。
func isBlockedPath(abs string) bool {
	norm := strings.ToLower(abs)
	norm = strings.ReplaceAll(norm, "/", `\`)

	// 盘符根的系统目录（前缀匹配）
	rootPrefixes := []string{
		`c:\windows`, `c:\program files`, `c:\programdata`, `c:\windows.old`,
		`c:\$recycle.bin`, `c:\system volume information`,
	}
	for _, rp := range rootPrefixes {
		if norm == rp || strings.HasPrefix(norm, rp+`\`) {
			return true
		}
	}

	// Unix 系统目录：要求位于路径段起始（前一个字符是盘符冒号或分隔符），
	// 避免误伤用户项目中的同名目录（如 proj/bin、proj/var）。
	segmentPrefixes := []string{
		`\etc`, `\usr`, `\boot`, `\dev`, `\proc`, `\sys`, `\var`,
		`\library`, `\system`, `\private`,
	}
	for _, sp := range segmentPrefixes {
		if strings.HasPrefix(norm, sp) {
			return true
		}
		if idx := strings.Index(norm, sp); idx > 0 {
			prev := norm[idx-1]
			if prev == ':' || prev == '\\' {
				return true
			}
		}
	}
	return false
}
