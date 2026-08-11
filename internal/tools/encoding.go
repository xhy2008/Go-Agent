package tools

import (
	"bufio"
	"io"
	"runtime"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// cmdScanner 返回逐行扫描器（不做转码，行解码交给 decodeCmdLine）。
func cmdScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	return s
}

// decodeCmdLine 将一行命令输出转为 UTF-8。
// Windows 下 cmd 输出为 GBK、PowerShell 可能输出 UTF-8 或 GBK：
// 按 UTF-8 合法性自动判别——合法视为 UTF-8 原样返回（但可能含双重编码乱码，
// 需要 FixMojibake 还原），否则按 GBK 解码。
// 非 Windows 平台原样返回。
func decodeCmdLine(line string) string {
	if runtime.GOOS != "windows" {
		return line
	}
	if utf8.ValidString(line) {
		return FixMojibake(line)
	}
	if b, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), []byte(line)); err == nil {
		return string(b)
	}
	return line
}

// mojibakeByte 建立 Windows-1252 字符 → 原始字节的映射，用于还原双重编码乱码。
// UTF-8 字节被按 Windows-1252/Latin-1 逐字节误解码后，0x80-0x9F 会被映射到
// U+20AC、U+0152 等特殊字符（如中文逗号 "，" 的 UTF-8 字节 EF BC 8C 变成 "ï¼Œ"）。
var mojibakeByte = func() map[rune]byte {
	m := make(map[rune]byte, 256)
	for r := rune(0x80); r <= 0xFF; r++ {
		m[r] = byte(r)
	}
	// Windows-1252 在 0x80-0x9F 的特殊映射（Latin-1 此范围未定义）。
	special := map[rune]byte{
		'\u20AC': 0x80, '\u201A': 0x82, '\u0192': 0x83, '\u201E': 0x84,
		'\u2026': 0x85, '\u2020': 0x86, '\u2021': 0x87, '\u02C6': 0x88,
		'\u2030': 0x89, '\u0160': 0x8A, '\u2039': 0x8B, '\u0152': 0x8C,
		'\u017D': 0x8E, '\u2018': 0x91, '\u2019': 0x92, '\u201C': 0x93,
		'\u201D': 0x94, '\u2022': 0x95, '\u2013': 0x96, '\u2014': 0x97,
		'\u02DC': 0x98, '\u2122': 0x99, '\u0161': 0x9A, '\u203A': 0x9B,
		'\u0153': 0x9C, '\u017E': 0x9E, '\u0178': 0x9F,
	}
	for r, b := range special {
		m[r] = b
	}
	return m
}()

// FixMojibake 还原 "UTF-8 字节被按 Windows-1252/Latin-1 逐字节解码后再 UTF-8 编码" 的双重编码乱码。
// 典型场景：PowerShell 抓取 UTF-8 网页时把字节按系统代码页误解码，中文变成 "å¤©"（天）一类。
// 规则：ASCII 字符保留；U+0080-U+00FF 或 Windows-1252 特殊字符还原为原始字节；
// 还原后的字节序列必须是合法 UTF-8 且与原字符串不同，才应用还原，避免误伤正常文本。
func FixMojibake(s string) string {
	if !utf8.ValidString(s) {
		return s
	}
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r < 0x80 {
			b = append(b, byte(r))
			continue
		}
		v, ok := mojibakeByte[r]
		if !ok {
			return s
		}
		b = append(b, v)
	}
	if utf8.Valid(b) && string(b) != s {
		return string(b)
	}
	return s
}
