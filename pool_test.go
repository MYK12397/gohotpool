package gohotpool

import (
	"bytes"
	"io"
	"sync"
	"testing"
)

func TestOptimized_Correctness(t *testing.T) {
	config := DefaultConfig()
	config.TrackStats = true // Enable for this test
	pool := NewPool(config)

	// Test basic Get/Put
	buf := pool.Get()
	buf.WriteString("test")
	if buf.String() != "test" {
		t.Error("Basic write failed")
	}
	pool.Put(buf)

	// Test reuse
	buf2 := pool.Get()
	if len(buf2.B) != 0 {
		t.Error("Buffer not reset")
	}
	pool.Put(buf2)
}

func TestOptimized_Concurrency(t *testing.T) {
	config := DefaultConfig()
	config.TrackStats = true // Enable for this test
	pool := NewPool(config)
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				buf := pool.Get()
				buf.WriteString("concurrent")
				pool.Put(buf)
			}
		}()
	}

	wg.Wait()
	stats := pool.GetStats()
	if stats.Gets != 100*1000 {
		t.Errorf("Expected 100000 Gets, got %d", stats.Gets)
	}
}

func TestOptimized_RingBuffer(t *testing.T) {
	config := DefaultConfig()
	config.TrackStats = true
	pool := NewPool(config)

	for i := 0; i < 100; i++ {
		buf := pool.GetCold()
		buf.WriteString("bulk")
		if len(buf.B) == 0 {
			t.Error("Ring buffer write failed")
		}
		pool.PutCold(buf) // NEW: Must put cold buffers back
	}

	stats := pool.GetStats()
	if stats.RingBufferUses != 100 {
		t.Errorf("Expected 100 ring buffer uses, got %d", stats.RingBufferUses)
	}
}

func TestOptimized_PinUnpin(t *testing.T) {
	pool := NewPool(DefaultConfig())
	buf := pool.Get()

	initialPin := buf.PinCount()
	pool.Pin(buf)
	if buf.PinCount() != initialPin+1 {
		t.Error("Pin failed")
	}

	pool.Unpin(buf)
	if buf.PinCount() != initialPin {
		t.Error("Unpin failed")
	}

	pool.Put(buf)
}

func TestOptimized_NoStats(t *testing.T) {
	config := DefaultConfig()
	config.TrackStats = false // Explicitly disabled (now default)
	pool := NewPool(config)

	buf := pool.Get()
	buf.WriteString("test")
	pool.Put(buf)

	// Should not panic
	stats := pool.GetStats()
	if stats.Gets != 0 {
		t.Error("Stats should be disabled")
	}
}

func TestOptimized_ReadFrom(t *testing.T) {
	pool := NewPool(DefaultConfig())
	buf := pool.Get()

	// Test ReadFrom with known data
	data := []byte("hello world this is a test")
	reader := bytes.NewReader(data)

	n, err := buf.ReadFrom(reader)
	if err != nil {
		t.Errorf("ReadFrom error: %v", err)
	}
	if n != int64(len(data)) {
		t.Errorf("ReadFrom returned wrong count: got %d, want %d", n, len(data))
	}
	if buf.String() != string(data) {
		t.Errorf("ReadFrom wrong content: got %q, want %q", buf.String(), string(data))
	}

	pool.Put(buf)
}

func TestOptimized_ReadFrom_LargeData(t *testing.T) {
	pool := NewPool(DefaultConfig())
	buf := pool.Get()

	// Test with data larger than initial buffer capacity
	data := make([]byte, 10000)
	for i := range data {
		data[i] = byte(i % 256)
	}
	reader := bytes.NewReader(data)

	n, err := buf.ReadFrom(reader)
	if err != nil {
		t.Errorf("ReadFrom error: %v", err)
	}
	if n != int64(len(data)) {
		t.Errorf("ReadFrom returned wrong count: got %d, want %d", n, len(data))
	}
	if !bytes.Equal(buf.B, data) {
		t.Error("ReadFrom data mismatch")
	}

	pool.Put(buf)
}

func TestOptimized_ColdBuffer_Concurrent(t *testing.T) {
	config := DefaultConfig()
	config.RingBufferSize = 8 // Small ring buffer to test contention
	pool := NewPool(config)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				buf := pool.GetCold()
				buf.WriteString("test")
				pool.PutCold(buf)
			}
		}()
	}
	wg.Wait()
}

func TestOptimized_WriteTo(t *testing.T) {
	pool := NewPool(DefaultConfig())
	buf := pool.Get()
	buf.WriteString("hello world")

	var out bytes.Buffer
	n, err := buf.WriteTo(&out)
	if err != nil {
		t.Errorf("WriteTo error: %v", err)
	}
	if n != int64(len("hello world")) {
		t.Errorf("WriteTo wrong count: got %d", n)
	}
	if out.String() != "hello world" {
		t.Errorf("WriteTo wrong content: got %q", out.String())
	}

	pool.Put(buf)
}

func TestOptimized_Grow(t *testing.T) {
	pool := NewPool(DefaultConfig())
	buf := pool.Get()

	initialCap := buf.Cap()
	buf.Grow(10000)
	if buf.Cap() < 10000 {
		t.Errorf("Grow failed: cap is %d, want >= 10000", buf.Cap())
	}
	if buf.Len() != 0 {
		t.Error("Grow changed length")
	}

	// Growing to smaller than current capacity should be no-op
	buf.Grow(100)
	if buf.Cap() < 10000 {
		t.Error("Grow shrunk the buffer")
	}

	_ = initialCap
	pool.Put(buf)
}

func TestOptimized_MarkDirty(t *testing.T) {
	pool := NewPool(DefaultConfig())
	buf := pool.Get()

	if buf.IsDirty() {
		t.Error("New buffer should be clean")
	}

	pool.MarkDirty(buf)
	if !buf.IsDirty() {
		t.Error("Buffer should be dirty after MarkDirty")
	}

	buf.Reset()
	if buf.IsDirty() {
		t.Error("Buffer should be clean after Reset")
	}

	pool.Put(buf)
}

func TestOptimized_DefaultPool(t *testing.T) {
	// Test package-level functions
	buf := Get()
	buf.WriteString("default pool test")
	if buf.String() != "default pool test" {
		t.Error("Default pool write failed")
	}
	Put(buf)

	// Test GetCold/PutCold
	buf2 := GetCold()
	buf2.WriteString("cold")
	PutCold(buf2)
}

// Ensure io.Writer and io.ReaderFrom interfaces are implemented
var _ io.Writer = (*Buffer)(nil)
var _ io.ReaderFrom = (*Buffer)(nil)
var _ io.WriterTo = (*Buffer)(nil)
