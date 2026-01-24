# GoHotPool

A PostgreSQL-inspired byte buffer pool for Go with advanced eviction strategies, pin/usage count mechanisms, and dirty buffer tracking.

**Keep your hot data hot, sweep the cold away.**

## Features

### 🎯 PostgreSQL-Inspired Design
- **Clock Sweep Eviction**: Smart eviction algorithm that keeps frequently-used buffers in memory
- **Pin/Usage Count Mechanism**: Prevents active buffers from being evicted and tracks access patterns
- **Dirty Buffer Tracking**: Monitors which buffers have been modified
- **Ring Buffers**: Isolated buffer pools for bulk operations to prevent cache pollution

### 📊 Rich Statistics
- Cache hit/miss rates
- Eviction counts and clock sweep operations
- Usage count distribution
- Dirty and pinned buffer tracking

### ⚡ Performance
- Lock-free fast path for common cases
- Concurrent-safe operations
- Zero allocation for buffer reuse
- Optimized for high-throughput scenarios

## Installation

```bash
go get github.com/yourusername/gohotpool
```

## Quick Start

```go
package main

import (
    "github.com/yourusername/gohotpool"
)

func main() {
    // Use default pool
    buf := gohotpool.Get()
    defer gohotpool.Put(buf)
    
    buf.WriteString("Hello, World!")
    println(buf.String())
}
```

## Advanced Usage

### Custom Pool Configuration

```go
config := gohotpool.Config{
    PoolSize:          1024,     // Number of buffers
    EnableRingBuffer:  true,      // Enable ring buffer for bulk ops
    RingBufferSize:    32,        // Ring buffer size
    DefaultBufferSize: 4096,      // Initial buffer capacity (4KB)
}

pool := gohotpool.NewPool(config)
```

### Clock Sweep Eviction

The pool uses PostgreSQL's clock sweep algorithm to intelligently evict cold buffers while protecting hot ones:

```go
pool := gohotpool.NewPool(gohotpool.DefaultConfig())

// Frequently accessed buffers get higher usage counts
for i := 0; i < 10; i++ {
    buf := pool.Get()
    buf.WriteString("hot data")
    pool.Put(buf)
    buf = pool.Get() // Same buffer likely returned, usage count increases
}

// Less frequently accessed buffers are evicted first
```

**How it works:**
1. Each buffer has a usage count (0-5)
2. On access, usage count increases (capped at 5)
3. During eviction, clock sweeps through buffers
4. Unpinned buffers with usage count > 0 are decremented
5. Buffers with usage count = 0 are evicted

### Pin/Unpin Protection

Protect buffers from eviction while in use:

```go
buf := pool.Get()

// Prevent eviction during critical operation
pool.Pin(buf)
defer pool.Unpin(buf)

// Do long-running work...
performSlowOperation(buf)

pool.Put(buf)
```

### Dirty Buffer Tracking

Track which buffers have been modified:

```go
buf := pool.Get()

buf.WriteString("data")
println(buf.IsDirty()) // true

buf.Reset()
println(buf.IsDirty()) // false

pool.Put(buf)
```

### Ring Buffers for Bulk Operations

Prevent bulk operations from polluting the main cache:

```go
config := gohotpool.Config{
    PoolSize:         1000,
    EnableRingBuffer: true,
    RingBufferSize:   50,  // Small ring for bulk ops
}
pool := gohotpool.NewPool(config)

// Bulk insert - uses ring buffer
for i := 0; i < 10000; i++ {
    buf := pool.GetCold()
    buf.WriteString(fmt.Sprintf("record %d", i))
    // Write to disk/network...
}

// Main cache remains hot for regular queries
stats := pool.GetStats()
fmt.Printf("Main pool evictions: %d\n", stats.Evictions) // Very low
```

## Real-World Examples

### HTTP Response Buffering

```go
pool := gohotpool.NewPool(gohotpool.DefaultConfig())

func handleRequest(w http.ResponseWriter, r *http.Request) {
    buf := pool.Get()
    defer pool.Put(buf)
    
    // Build response
    buf.WriteString("HTTP/1.1 200 OK\r\n")
    buf.WriteString("Content-Type: application/json\r\n\r\n")
    
    json.NewEncoder(buf).Encode(responseData)
    
    w.Write(buf.Bytes())
}
```

### CSV Processing

```go
func processLargeCSV(filename string) error {
    pool := gohotpool.NewPool(gohotpool.Config{
        EnableRingBuffer: true,
        RingBufferSize:   100,
    })
    
    file, _ := os.Open(filename)
    defer file.Close()
    
    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        // Use ring buffer for bulk processing
        buf := pool.GetCold()
        buf.WriteString(scanner.Text())
        
        // Process row...
        processRow(buf.Bytes())
    }
    
    return nil
}
```

### Log Aggregation

```go
type LogAggregator struct {
    pool *gohotpool.Pool
}

func (la *LogAggregator) AddLog(message string) {
    buf := la.pool.Get()
    defer la.pool.Put(buf)
    
    // Format log entry
    buf.WriteString(time.Now().Format(time.RFC3339))
    buf.WriteString(" ")
    buf.WriteString(message)
    
    // Frequently accessed recent logs stay in cache
    la.storage.Write(buf.Bytes())
}
```

## Statistics and Monitoring

```go
pool := gohotpool.NewPool(gohotpool.DefaultConfig())

// ... use pool ...

stats := pool.GetStats()

fmt.Printf("Cache hit rate: %.2f%%\n", 
    float64(stats.Hits)/float64(stats.Hits+stats.Misses)*100)
fmt.Printf("Evictions: %d\n", stats.Evictions)
fmt.Printf("Clock sweeps: %d\n", stats.ClockSweeps)
fmt.Printf("Average usage count: %.2f\n", stats.AvgUsageCount)
fmt.Printf("Pinned buffers: %d\n", stats.PinnedBuffers)
fmt.Printf("Ring buffer uses: %d\n", stats.RingBufferUses)
```

## Performance Comparison

**Benchmark Results** (Intel Core i5-10210U @ 1.60GHz):

```
BenchmarkGoHotPool-8                    46,868      25,148 ns/op
BenchmarkSyncPool-8                232,297,639       5.393 ns/op
BenchmarkGoHotPool_RingBuffer-8      6,688,333       179.7 ns/op
BenchmarkClockSweep_LowContention-8  7,357,452       155.6 ns/op
BenchmarkClockSweep_HighContention-8 6,903,734       160.5 ns/op
```

### ⚠️ Performance Reality Check

**sync.Pool is ~4,600x faster** for simple pooling because it:
- Has zero tracking overhead
- No mutex contention on the fast path
- GC-aware design optimized by the Go runtime

**gohotpool trades raw speed for intelligence:**
- Clock sweep eviction: ~25µs per operation
- Ring buffer (low contention): ~180ns per operation
- Statistics tracking adds overhead

### 📊 When Each Pool Makes Sense

| Use Case | Best Choice | Why |
|----------|-------------|-----|
| **Simple buffer reuse** | sync.Pool | 4,600x faster, good enough |
| **Hot data workload** | gohotpool | Keeps frequently-accessed buffers |
| **Bulk operations** | gohotpool.GetCold() | Ring buffer prevents cache pollution |
| **Need statistics** | gohotpool | Built-in metrics, sync.Pool has none |
| **Predictable memory** | gohotpool | Fixed pool size, sync.Pool varies with GC |
| **Maximum performance** | sync.Pool | No contest for raw speed |

### ✅ When to Use GoHotPool

**Use gohotpool when you NEED:**
1. **Observable pooling** - Statistics, monitoring, debugging
2. **Ring buffer isolation** - Bulk operations that would pollute cache
3. **Predictable memory** - Fixed pool size, not GC-dependent
4. **Hot/cold separation** - `Get()` for hot, `GetCold()` for bulk
5. **Pin protection** - Prevent eviction during critical operations

**Use sync.Pool when:**
- Raw performance is critical
- Simple pooling is sufficient
- GC-based lifecycle is acceptable
- Don't need statistics

### 🎯 The Honest Trade-off

```go
// sync.Pool: Fast but opaque
pool := &sync.Pool{New: func() interface{} { return &bytes.Buffer{} }}
buf := pool.Get().(*bytes.Buffer)  // ~5ns
defer pool.Put(buf)

// gohotpool: Slower but intelligent
buf := gohotpool.Get()              // ~25µs
defer gohotpool.Put(buf)
stats := gohotpool.GetStats()       // Know what's happening
```

**The ~25µs overhead is acceptable when:**
- You're doing actual work with the buffer (I/O, parsing, etc.)
- You need the observability features
- You want predictable memory usage
- Your workload benefits from hot/cold separation

**The overhead is NOT acceptable when:**
- You're just pooling for GC reduction
- Your buffer operations are < 1µs
- You don't need any of the smart features

## API Reference

### Pool Methods

- `Get() *Buffer` - Get a buffer from the pool
- `Put(buf *Buffer)` - Return buffer to pool
- `GetRingBuffer() *Buffer` - Get buffer from ring buffer
- `Pin(buf *Buffer)` - Prevent buffer eviction
- `Unpin(buf *Buffer)` - Allow buffer eviction
- `MarkDirty(buf *Buffer)` - Mark buffer as modified
- `GetStats() PoolStats` - Get pool statistics

### Buffer Methods

- `Write(p []byte) (int, error)` - Append bytes
- `WriteString(s string) (int, error)` - Append string
- `WriteByte(c byte) error` - Append single byte
- `Bytes() []byte` - Get buffer contents
- `String() string` - Get as string
- `Len() int` - Get length
- `Reset()` - Clear buffer
- `ReadFrom(r io.Reader) (int64, error)` - Read from reader
- `WriteTo(w io.Writer) (int64, error)` - Write to writer
- `IsDirty() bool` - Check if modified
- `UsageCount() int32` - Get usage count
- `PinCount() int32` - Get pin count

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

MIT License - see LICENSE file for details

## Acknowledgments

Inspired by:
- PostgreSQL's buffer management system
- [valyala/bytebufferpool](https://github.com/valyala/bytebufferpool)
- [vmihailenco/bufpool](https://github.com/vmihailenco/bufpool)

## Related Articles

- [Introduction to Buffers in PostgreSQL](https://boringsql.com/posts/introduction-to-buffers/)