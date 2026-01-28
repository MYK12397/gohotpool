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
- Per-P cache hit tracking
- Dirty and pinned buffer tracking
 
### ⚡ Performance (v2.0 - Optimized)
- **Per-P (processor) local caching** - Like sync.Pool, near-zero contention
- **Lock-free fast path** using atomic operations
- **Sharded pool** to reduce mutex contention
- Zero allocation for buffer reuse
- **Near sync.Pool performance** in single-goroutine scenarios
 
## Installation
 
```bash
go get github.com/MYK12397/gohotpool
```
 
## Quick Start
 
```go
package main
 
import (
    "github.com/MYK12397/gohotpool"
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
    PoolSize:          1024,      // Number of buffers
    ShardCount:        16,        // Number of shards (power of 2)
    EnableRingBuffer:  true,      // Enable ring buffer for bulk ops
    RingBufferSize:    32,        // Ring buffer size
    DefaultBufferSize: 4096,      // Initial buffer capacity (4KB)
    TrackStats:        false,     // Disable for max performance
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
    pool.PutCold(buf) // Return to ring buffer
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
        pool.PutCold(buf)
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
config := gohotpool.DefaultConfig()
config.TrackStats = true // Enable stats (disabled by default for performance)
pool := gohotpool.NewPool(config)
 
// ... use pool ...
 
stats := pool.GetStats()
 
fmt.Printf("Cache hit rate: %.2f%%\n", 
    float64(stats.Hits)/float64(stats.Hits+stats.Misses)*100)
fmt.Printf("Per-P cache hits: %d\n", stats.PerPHits)
fmt.Printf("Evictions: %d\n", stats.Evictions)
fmt.Printf("Clock sweeps: %d\n", stats.ClockSweeps)
fmt.Printf("Ring buffer uses: %d\n", stats.RingBufferUses)
```
 
## Performance Comparison
 
### Benchmark Results (Apple M4 Pro, Go 1.23.1)
 
```
goos: darwin
goarch: arm64
cpu: Apple M4 Pro
 
Benchmark                              ns/op    allocs/op  vs sync.Pool
------------------------------------------------------------------------
BenchmarkOptimized_NoStats_Parallel     5.2         0      ~4x slower
BenchmarkOptimized_PerPCache_Parallel   4.5         0      ~4x slower
BenchmarkSyncPool_Parallel              1.2         0      baseline
 
BenchmarkOptimized_SingleGoroutine      7.7         0      SAME SPEED! ✓
BenchmarkSyncPool_SingleGoroutine       7.5         0      baseline
 
BenchmarkOptimized_HTTPResponse         7.9         0      ~4x slower
BenchmarkSyncPool_HTTPResponse          2.0         0      baseline
 
BenchmarkOptimized_BulkCSV              4.9         0      FASTER! ✓
BenchmarkSyncPool_BulkCSV               8.5         0      baseline
 
BenchmarkOptimized_HighContention       4.3         0      ~4x slower
BenchmarkOptimized_LowContention        5.3         0      ~4x slower
BenchmarkOptimized_ManyShards           6.2         0      ~5x slower
```
 
### Performance Highlights 🚀
 
| Scenario | gohotpool | sync.Pool | Winner |
|----------|-----------|-----------|--------|
| **Single-goroutine** | 7.7 ns | 7.5 ns | **Tie!** |
| **Bulk CSV** | 4.9 ns | 8.5 ns | **gohotpool** |
| **High contention** | 4.3 ns | ~1 ns | sync.Pool |
| **Parallel (no stats)** | 5.2 ns | 1.2 ns | sync.Pool |
 
### 📊 When Each Pool Makes Sense
 
| Use Case | Best Choice | Why |
|----------|-------------|-----|
| **Single-goroutine workloads** | **Either** | Same performance! |
| **Bulk/batch operations** | **gohotpool** | Ring buffer + faster in benchmarks |
| **Hot data workload** | **gohotpool** | Keeps frequently-accessed buffers |
| **Need statistics** | **gohotpool** | Built-in metrics, sync.Pool has none |
| **Predictable memory** | **gohotpool** | Fixed pool size, sync.Pool varies with GC |
| **Maximum parallel throughput** | sync.Pool | ~4x faster under heavy contention |
 
### ✅ When to Use GoHotPool
 
**Use gohotpool when you NEED:**
1. **Observable pooling** - Statistics, monitoring, debugging
2. **Ring buffer isolation** - Bulk operations that would pollute cache
3. **Predictable memory** - Fixed pool size, not GC-dependent
4. **Hot/cold separation** - `Get()` for hot, `GetCold()` for bulk
5. **Pin protection** - Prevent eviction during critical operations
6. **Single-goroutine workloads** - Same speed as sync.Pool!
 
**Use sync.Pool when:**
- Maximum parallel throughput is critical
- Simple pooling is sufficient
- GC-based lifecycle is acceptable
- Don't need statistics or smart eviction
 
### 🎯 The Honest Trade-off
 
```go
// sync.Pool: Fast parallel, opaque
pool := &sync.Pool{New: func() interface{} { return &bytes.Buffer{} }}
buf := pool.Get().(*bytes.Buffer)  // ~1ns parallel, ~7.5ns single
defer pool.Put(buf)
 
// gohotpool: Same speed single-threaded, intelligent features
buf := gohotpool.Get()              // ~5ns parallel, ~7.7ns single
defer gohotpool.Put(buf)
stats := gohotpool.GetStats()       // Know what's happening
```
 
## API Reference
 
### Pool Methods
 
- `Get() *Buffer` - Get a buffer from the pool (uses per-P cache fast path)
- `Put(buf *Buffer)` - Return buffer to pool
- `GetCold() *Buffer` - Get buffer from ring buffer (for bulk operations)
- `PutCold(buf *Buffer)` - Return buffer to ring buffer
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
- `Cap() int` - Get capacity
- `Grow(n int)` - Ensure n bytes of unused capacity
- `Reset()` - Clear buffer
- `ReadFrom(r io.Reader) (int64, error)` - Read from reader
- `WriteTo(w io.Writer) (int64, error)` - Write to writer
- `IsDirty() bool` - Check if modified
- `UsageCount() int32` - Get usage count
- `PinCount() int32` - Get pin count
 
### Package-level Functions
 
```go
gohotpool.Get()           // Get from default pool
gohotpool.Put(buf)        // Return to default pool
gohotpool.GetCold()       // Get from default ring buffer
gohotpool.PutCold(buf)    // Return to default ring buffer
gohotpool.GetStats()      // Get default pool stats
gohotpool.Pin(buf)        // Pin buffer in default pool
gohotpool.Unpin(buf)      // Unpin buffer in default pool
gohotpool.MarkDirty(buf)  // Mark buffer dirty in default pool
```
 
## Contributing
 
Contributions are welcome! Please feel free to submit a Pull Request.
 
## License
 
MIT License - see LICENSE file for details
 