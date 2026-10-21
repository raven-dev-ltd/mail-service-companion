package companion

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func TestLabelKeywordFitsDefaultDovecotWithoutLosingIDBits(t *testing.T) {
	for _, id := range []string{strings.Repeat("0", 64), strings.Repeat("f", 64), "42bc313b699696f1e666d4e4665cfb16f33d4d47a627aa7acf5ca7289ae6e970"} {
		keyword := labelKeyword(id)
		if len(keyword) != 46 || !strings.HasPrefix(keyword, "mm_") {
			t.Fatalf("label keyword length = %d, want 46", len(keyword))
		}
		data, err := base64.RawURLEncoding.DecodeString(keyword[3:])
		if err != nil || hex.EncodeToString(data) != id {
			t.Fatal("label ID did not round-trip")
		}
	}
	for _, id := range []string{"", "bad", strings.Repeat("z", 64)} {
		if labelKeyword(id) != "" {
			t.Fatal("invalid label ID was accepted")
		}
	}
}
