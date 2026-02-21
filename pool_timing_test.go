package gohotpool

import (
	"bytes"
	"sync"
	"testing"
)

func BenchmarkOptimized_Get_Parallel(b *testing.B) {
	config := DefaultConfig()
	config.TrackStats = true // Include stats overhead
	pool := NewPool(config)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := pool.Get()
			buf.WriteString("benchmark")
			pool.Put(buf)
		}
	})
}

func BenchmarkOptimized_GetCold_Parallel(b *testing.B) {
	pool := NewPool(DefaultConfig())
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := pool.GetCold()
			buf.WriteString("benchmark")
			pool.PutCold(buf)
		}
	})
}

func BenchmarkOptimized_NoStats_Parallel(b *testing.B) {
	config := DefaultConfig()
	config.TrackStats = false
	pool := NewPool(config)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := pool.Get()
			buf.WriteString("benchmark")
			pool.Put(buf)
		}
	})
}

// BenchmarkOptimized_PerPCache_Parallel tests the per-P cache fast path
func BenchmarkOptimized_PerPCache_Parallel(b *testing.B) {
	config := DefaultConfig()
	config.TrackStats = false
	pool := NewPool(config)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := pool.Get()
			buf.WriteString("benchmark")
			pool.Put(buf)
		}
	})
}

func BenchmarkOptimized_ManyShards_Parallel(b *testing.B) {
	config := DefaultConfig()
	config.ShardCount = 64
	config.TrackStats = false
	pool := NewPool(config)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := pool.Get()
			buf.WriteString("benchmark")
			pool.Put(buf)
		}
	})
}

func BenchmarkSyncPool_Parallel(b *testing.B) {
	pool := &sync.Pool{
		New: func() interface{} {
			return &bytes.Buffer{}
		},
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := pool.Get().(*bytes.Buffer)
			buf.WriteString("benchmark")
			buf.Reset()
			pool.Put(buf)
		}
	})
}

func BenchmarkOptimized_HTTPResponse(b *testing.B) {
	config := DefaultConfig()
	config.TrackStats = false
	pool := NewPool(config)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := pool.Get()
			buf.WriteString("HTTP/1.1 200 OK\r\n")
			buf.WriteString("Content-Type: application/json\r\n")
			buf.WriteString("Content-Length: 100\r\n\r\n")
			buf.WriteString(`{"status":"ok","data":[1123,21231,31231,41231,51231]}`)
			_ = buf.Bytes()
			pool.Put(buf)
		}
	})
}

func BenchmarkSyncPool_HTTPResponse(b *testing.B) {
	pool := &sync.Pool{
		New: func() interface{} {
			return &bytes.Buffer{}
		},
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := pool.Get().(*bytes.Buffer)
			buf.WriteString("HTTP/1.1 200 OK\r\n")
			buf.WriteString("Content-Type: application/json\r\n")
			buf.WriteString("Content-Length: 100\r\n\r\n")
			buf.WriteString(`{"status":"ok","data":[1,2,3,4,5]}`)
			_ = buf.Bytes()
			buf.Reset()
			pool.Put(buf)
		}
	})
}

func BenchmarkOptimized_BulkCSV(b *testing.B) {
	pool := NewPool(DefaultConfig())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := pool.GetCold()
		buf.WriteString("1,fil dune,fdune@example.com,100\n")
		_ = buf.Bytes()
		pool.PutCold(buf)
	}
}

func BenchmarkSyncPool_BulkCSV(b *testing.B) {
	pool := &sync.Pool{
		New: func() interface{} {
			return &bytes.Buffer{}
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := pool.Get().(*bytes.Buffer)
		buf.WriteString("1,fil dune,fdune@example.com,100\n")
		_ = buf.Bytes()
		buf.Reset()
		pool.Put(buf)
	}
}

func BenchmarkOptimized_HighContention(b *testing.B) {
	config := DefaultConfig()
	config.PoolSize = 10 // Small pool
	config.TrackStats = false
	pool := NewPool(config)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := pool.Get()
			buf.WriteString("test")
			pool.Put(buf)
		}
	})
}

func BenchmarkOptimized_LowContention(b *testing.B) {
	config := DefaultConfig()
	config.PoolSize = 10000 // Large pool
	config.TrackStats = false
	pool := NewPool(config)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := pool.Get()
			buf.WriteString("test")
			pool.Put(buf)
		}
	})
}

func BenchmarkOptimized_SmallWrites(b *testing.B) {
	config := DefaultConfig()
	config.TrackStats = false
	pool := NewPool(config)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := pool.Get()
			buf.WriteString("test")
			pool.Put(buf)
		}
	})
}

func BenchmarkOptimized_MediumWrites(b *testing.B) {
	config := DefaultConfig()
	config.TrackStats = false
	pool := NewPool(config)
	data := make([]byte, 1024)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := pool.Get()
			buf.Write(data)
			pool.Put(buf)
		}
	})
}

func BenchmarkOptimized_LargeWrites(b *testing.B) {
	config := DefaultConfig()
	config.TrackStats = false
	pool := NewPool(config)
	data := make([]byte, 64*1024)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := pool.Get()
			buf.Write(data)
			pool.Put(buf)
		}
	})
}

// BenchmarkOptimized_GetPut_SingleGoroutine tests single-goroutine performance
// to measure the true fast-path latency without contention
func BenchmarkOptimized_GetPut_SingleGoroutine(b *testing.B) {
	config := DefaultConfig()
	config.TrackStats = false
	pool := NewPool(config)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := pool.Get()
		buf.WriteString("test")
		pool.Put(buf)
	}
}

func BenchmarkSyncPool_SingleGoroutine(b *testing.B) {
	pool := &sync.Pool{
		New: func() interface{} {
			return &bytes.Buffer{}
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := pool.Get().(*bytes.Buffer)
		buf.WriteString("test")
		buf.Reset()
		pool.Put(buf)
	}
}

func BenchmarkComparison_Generate(b *testing.B) {
	b.Skip("Run individually to generate comparison table")

	// Run this to generate comparison table:
	// go test -bench=. -benchmem -benchtime=3s
}
