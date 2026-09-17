package engine

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRedisPreviewTextKeepsUTF8WithinLimit(t *testing.T) {
	value := strings.Repeat("ş", 200)
	got := redisPreviewText(value, 255)
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") || len(got) > 258 {
		t.Fatalf("invalid preview %q (%d bytes)", got, len(got))
	}
	if got := redisPreviewText("short", 255); got != "short" {
		t.Fatalf("short value changed: %q", got)
	}
}
