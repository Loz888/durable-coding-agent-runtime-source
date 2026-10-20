// Package boundedlog retains the beginning and latest failure evidence within one byte cap.
package boundedlog

import "sync"

const marker = "\n...[middle output omitted]...\n"

type Writer struct {
	mu                   sync.Mutex
	head, tail           []byte
	headLimit, tailLimit int
	total                int64
}

func New(limit int) *Writer {
	usable := limit - len(marker)
	if usable < 2 {
		usable = 2
	}
	return &Writer{headLimit: usable / 2, tailLimit: usable - usable/2}
}
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	w.total += int64(n)
	if left := w.headLimit - len(w.head); left > 0 {
		if left > len(p) {
			left = len(p)
		}
		w.head = append(w.head, p[:left]...)
		p = p[left:]
	}
	if len(p) >= w.tailLimit {
		w.tail = append(w.tail[:0], p[len(p)-w.tailLimit:]...)
	} else {
		if drop := len(w.tail) + len(p) - w.tailLimit; drop > 0 {
			copy(w.tail, w.tail[drop:])
			w.tail = w.tail[:len(w.tail)-drop]
		}
		w.tail = append(w.tail, p...)
	}
	return n, nil
}
func (w *Writer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.total > int64(w.headLimit+w.tailLimit) {
		return string(w.head) + marker + string(w.tail)
	}
	return string(w.head) + string(w.tail)
}
func Clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	if limit < len(marker)+2 {
		return text[:limit]
	}
	w := New(limit)
	_, _ = w.Write([]byte(text))
	return w.String()
}
