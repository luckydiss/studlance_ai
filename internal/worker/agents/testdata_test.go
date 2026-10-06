package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Пункт 8 (второй раунд): репозиторий публичный, поэтому в testdata не должно
// быть конкретных идентификаторов моделей (поля model, ключи modelUsage,
// canonicalModel) и личных путей/имён. Логи записаны на выдуманных заданиях,
// пути пользователя заменены на C:\work\job.
func TestTestdataIsSanitized(t *testing.T) {
	modelRe := regexp.MustCompile(`claude-(sonnet|opus|haiku)-[0-9]|gpt-[0-9]|codex-mini|o[0-9]-mini`)
	personal := []string{
		"luckydiss",
		`C:\rec\`,
		`C:/rec/`,
		"Documents\\studlance",
		`C:\Users\luckydiss`,
	}
	files, err := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no fixtures found")
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if m := modelRe.FindString(text); m != "" {
			t.Errorf("%s contains a model identifier %q", filepath.Base(path), m)
		}
		for _, p := range personal {
			if strings.Contains(text, p) {
				t.Errorf("%s contains a personal marker %q", filepath.Base(path), p)
			}
		}
		for i, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !json.Valid([]byte(line)) {
				t.Errorf("%s:%d is not valid JSON", filepath.Base(path), i+1)
			}
		}
	}
}
