package db

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// 先前回傳的結果不得被後續的就地寫入改變
func TestSnapshotImmutability(t *testing.T) {
	resetConfig()
	setonly("a", "1111")
	setonly("b", "2222")

	leaf := Get("a")
	whole := Get("")

	setonly("a", "9999")                   // 同長度 -> 走就地覆寫
	setonly("b", strings.Repeat("x", 500)) // 不同長度 -> 走重建

	if leaf != `"1111"` {
		t.Fatalf("先前讀到的葉節點被改掉了: %s", leaf)
	}
	if !strings.Contains(whole, `"a":"1111"`) {
		t.Fatalf("先前讀到的整份文件被改掉了: %s", whole)
	}
	if got := Get("a"); got != `"9999"` {
		t.Fatalf("新值不正確: %s", got)
	}
}

// 併發讀寫下不得出現破損的 JSON 或撕裂的值
func TestConcurrentReadWrite(t *testing.T) {
	resetConfig()
	for i := 0; i < 50; i++ {
		setonly(fmt.Sprintf("k%d", i), strings.Repeat("v", 20))
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				setonly(fmt.Sprintf("k%d", i%50), strings.Repeat("v", 10+i%40))
			}
		}(w)
	}
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20000; i++ {
				whole := Get("")
				if !strings.HasPrefix(whole, "{") || !strings.HasSuffix(whole, "}") {
					t.Errorf("讀到破損的文件: %.60s", whole)
					return
				}
				v := Get("k7")
				if v != "" && !strings.HasPrefix(v, `"v`) {
					t.Errorf("讀到撕裂的值: %q", v)
					return
				}
			}
		}()
	}
	close(stop)
	wg.Wait()
}

// TestEscapedValueWrite guards a trap in sjson's in-place path: asked to
// stringify a value needing escapes, it returns the document unchanged and
// reports no error. Writing pre-rendered raw JSON avoids that branch, so every
// value below must land even though it fits in the slot it replaces.
func TestEscapedValueWrite(t *testing.T) {
	for _, c := range []struct{ name, val string }{
		{"quote", `a"b`},
		{"backslash", `a\b`},
		{"control", "a\nb"},
		{"nonascii", "中文字"},
		{"plain", "abcd"},
	} {
		t.Run(c.name, func(t *testing.T) {
			resetConfig()
			setonly("k", "xxxxxxxxxxxxxxxxxxxx")
			if err := setonly("k", c.val); err != nil {
				t.Fatalf("setonly: %v", err)
			}
			var got string
			if err := json.Unmarshal([]byte(Get("k")), &got); err != nil {
				t.Fatalf("unmarshal %q: %v", Get("k"), err)
			}
			if got != c.val {
				t.Errorf("got %q, want %q", got, c.val)
			}
		})
	}
}
