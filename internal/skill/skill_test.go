package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleSkill = `---
name: "python-expert"
description: "适用于Python项目的开发助手"
triggers: ["python", "pip", "venv"]
version: "1.0"
---
# 角色指令
你是一个Python专家，擅长虚拟环境和包管理。`

func TestParseFrontmatter(t *testing.T) {
	s := Parse(sampleSkill, "test.md")
	if s.Name != "python-expert" {
		t.Errorf("name = %q", s.Name)
	}
	if s.Description != "适用于Python项目的开发助手" {
		t.Errorf("description = %q", s.Description)
	}
	if len(s.Triggers) != 3 || s.Triggers[0] != "python" {
		t.Errorf("triggers = %v", s.Triggers)
	}
	if s.Version != "1.0" {
		t.Errorf("version = %q", s.Version)
	}
	if !strings.Contains(s.Body, "Python专家") {
		t.Errorf("body = %q", s.Body)
	}
}

func TestMatch(t *testing.T) {
	s := Parse(sampleSkill, "test.md")
	if !s.Match("帮我装个 python 包") {
		t.Error("should match python")
	}
	if s.Match("帮我看看 Go 代码") {
		t.Error("should not match")
	}
}

func TestLoadFromDir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, ".agent", "skills")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "python.md"), []byte(sampleSkill), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "note.txt"), []byte("not a skill"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "python-expert" {
		t.Errorf("name = %q", skills[0].Name)
	}

	// 目录不存在时返回空
	skills, err = Load(filepath.Join(t.TempDir(), "nonexist"))
	if err != nil || len(skills) != 0 {
		t.Errorf("expected empty, got %v %v", skills, err)
	}
}

func TestNoFrontmatter(t *testing.T) {
	s := Parse("你是一个通用助手。", "a.md")
	if s.Name != "" {
		t.Errorf("name should be empty, got %q", s.Name)
	}
	if s.Body != "你是一个通用助手。" {
		t.Errorf("body = %q", s.Body)
	}
}
