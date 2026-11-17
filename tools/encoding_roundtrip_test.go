package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func gbkBytes(t *testing.T, s string) []byte {
	t.Helper()
	b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("encode %q to GBK: %v", s, err)
	}
	return b
}

func TestEditFileGBKRoundTrip(t *testing.T) {
	src := "// 配置项\n名称 := \"旧值\"\n"
	dir := t.TempDir()
	fp := filepath.Join(dir, "conf.go")
	if err := os.WriteFile(fp, gbkBytes(t, src), 0644); err != nil {
		t.Fatal(err)
	}

	read, err := NewReadFileTool(dir).Execute(context.Background(), map[string]interface{}{"path": "conf.go"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(read.Text, "名称") {
		t.Fatalf("read_file did not decode GBK: %s", read.Text)
	}

	out, err := NewEditFileTool(dir).Execute(context.Background(), map[string]interface{}{
		"path":     "conf.go",
		"old_text": "名称 := \"旧值\"",
		"new_text": "名称 := \"新值\"",
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !strings.Contains(out.Text, "has been updated successfully") {
		t.Fatalf("GBK edit failed: %s", out.Text)
	}

	got, err := os.ReadFile(fp)
	if err != nil {
		t.Fatal(err)
	}
	want := gbkBytes(t, "// 配置项\n名称 := \"新值\"\n")
	if string(got) != string(want) {
		t.Fatalf("file encoding not preserved as GBK:\ngot:  %q\nwant: %q", string(got), string(want))
	}
}

func TestWriteFilePreservesGBKOnOverwrite(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(fp, gbkBytes(t, "旧内容\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := NewWriteFileTool(dir).Execute(context.Background(), map[string]interface{}{
		"path":    "a.txt",
		"content": "新内容\n",
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out.Text, "Updated file") {
		t.Fatalf("write failed: %s", out.Text)
	}
	got, _ := os.ReadFile(fp)
	if string(got) != string(gbkBytes(t, "新内容\n")) {
		t.Fatalf("overwrite transcoded the file: %q", string(got))
	}
}

func TestWriteFileAppendPreservesGBK(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "log.txt")
	if err := os.WriteFile(fp, gbkBytes(t, "第一行\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWriteFileTool(dir).Execute(context.Background(), map[string]interface{}{
		"path":    "log.txt",
		"content": "第二行\n",
		"append":  true,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, _ := os.ReadFile(fp)
	if string(got) != string(gbkBytes(t, "第一行\n第二行\n")) {
		t.Fatalf("append mixed encodings: %q", string(got))
	}
}

func TestWriteFileNewFileIsUTF8(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewWriteFileTool(dir).Execute(context.Background(), map[string]interface{}{
		"path":    "new.go",
		"content": "package 新\n",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "new.go"))
	if string(got) != "package 新\n" {
		t.Fatalf("new file is not UTF-8: %q", string(got))
	}
}

func TestEditFileUTF8BOMKept(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "bom.txt")
	src := append([]byte{0xEF, 0xBB, 0xBF}, []byte("hello\n")...)
	if err := os.WriteFile(fp, src, 0644); err != nil {
		t.Fatal(err)
	}
	read, err := NewReadFileTool(dir).Execute(context.Background(), map[string]interface{}{"path": "bom.txt"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(read.Text, "1: hello") {
		t.Fatalf("BOM leaked into the first line: %s", read.Text)
	}
	if _, err := NewEditFileTool(dir).Execute(context.Background(), map[string]interface{}{
		"path": "bom.txt", "old_text": "hello", "new_text": "bye",
	}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	got, _ := os.ReadFile(fp)
	want := append([]byte{0xEF, 0xBB, 0xBF}, []byte("bye\n")...)
	if string(got) != string(want) {
		t.Fatalf("BOM not preserved: %q", string(got))
	}
}

func TestEditFileEmojiInGBKFileFallsBackToUTF8(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "e.txt")
	if err := os.WriteFile(fp, gbkBytes(t, "状态: 旧\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := NewEditFileTool(dir).Execute(context.Background(), map[string]interface{}{
		"path": "e.txt", "old_text": "状态: 旧", "new_text": "状态: 新 🙂",
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !strings.Contains(out.Text, "has been updated successfully") {
		t.Fatalf("edit failed: %s", out.Text)
	}
	got, _ := os.ReadFile(fp)
	if string(got) != "状态: 新 🙂\n" {
		t.Fatalf("fallback did not write valid UTF-8: %q", string(got))
	}
}
