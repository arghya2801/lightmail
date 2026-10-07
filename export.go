package main

import (
	"encoding/base64"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func day(unix int64) string { return time.Unix(unix, 0).Format("2006-01-02") }

func fileName(m Msg, ext string) string {
	name := fmt.Sprintf("%s %s - %s", day(m.Date), m.FromName, m.Subject)
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return -1
		}
		return r
	}, name)
	if r := []rune(name); len(r) > 150 {
		name = string(r[:150])
	}
	return strings.TrimRight(strings.TrimSpace(name), ".") + ext
}

// saveEmail writes m to saveDir as self-contained HTML, images inlined.
func saveEmail(dataDir, saveDir string, m Msg) (string, error) {
	if err := os.MkdirAll(saveDir, 0o755); err != nil {
		return "", err
	}
	out := rewrite(m.HTML, func(u string) string {
		b, ct, err := fetchImage(dataDir, u)
		if err != nil {
			return u
		}
		return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(b)
	})
	out = strings.Replace(out, "<head>", "<head><meta charset=\"utf-8\"><title>"+html.EscapeString(m.Subject)+"</title>", 1)
	path := filepath.Join(saveDir, fileName(m, ".html"))
	return path, os.WriteFile(path, []byte(out), 0o644)
}
