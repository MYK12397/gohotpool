package gohotpool

import (
	"sync"
	"testing"
)

func TestOptimized_Correctness(t *testing.T) {
	pool := NewPool(DefaultConfig())

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
	pool := NewPool(DefaultConfig())
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
	pool := NewPool(DefaultConfig())

	for i := 0; i < 100; i++ {
		buf := pool.GetCold()
		buf.WriteString("bulk")
		if len(buf.B) == 0 {
			t.Error("Ring buffer write failed")
		}
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
	config.TrackStats = false
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
