package boundedlog

import (
	"strings"
	"sync"
	"testing"
)

func TestLongNoiseKeepsLatestFailureAndByteBound(t *testing.T) {
	s := "source commit evidence\n" + strings.Repeat("noise\n", 30000) + "math.go:12 divide by zero\nFAIL\n"
	out := Clip(s, 8000)
	if len(out) > 8000 || !strings.Contains(out, "source commit") || !strings.Contains(out, "math.go:12 divide by zero") || !strings.Contains(out, "omitted") {
		t.Fatal("lost failure evidence or byte cap")
	}
}
func TestConcurrentWritersStayBounded(t *testing.T) {
	w := New(65536)
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for n := 0; n < 100; n++ {
				_, _ = w.Write([]byte(strings.Repeat("x", 1000)))
			}
		}()
	}
	group.Wait()
	if len(w.String()) > 65536 {
		t.Fatal("unbounded output")
	}
}
