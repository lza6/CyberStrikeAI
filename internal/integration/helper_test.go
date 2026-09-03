package integration_test

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempMicroagents 在临时目录写入若干 microagent .md 文件，返回目录路径。
func writeTempMicroagents(t *testing.T, files map[string]string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "microagent-it-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
