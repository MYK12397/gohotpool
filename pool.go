// Package gohotpool - Optimized version with near sync.Pool performance
package gohotpool

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const (
	MaxUsageCount         = 5
	DefaultPoolSize       = 1024
	DefaultRingBufferSize = 32
	DefaultShardCount     = 16 // New: shard the pool to reduce contention
)

// BufferState represents the state of a buffer
type BufferState uint32

const (
	StateClean BufferState = iota
	StateDirty
	StateWriting
)

// BufferDescriptor - optimized with atomic operations instead of mutex
type BufferDescriptor struct {
	usageCount uint32 // atomic, 0-5
	pinCount   uint32 // atomic
	state      uint32 // atomic BufferState
	lastUsed   int64  // atomic unix nano
	totalUses  uint64 // atomic
	valid      uint32 // atomic bool (0 or 1)
}

// Buffer represents a byte buffer with metadata
type Buffer struct {
	B    []byte
	desc *BufferDescriptor
	pool *Pool
}

// Shard - each shard has its own lock to reduce contention
type Shard struct {
	buffers     []*Buffer
	descriptors []*BufferDescriptor
	clockHand   uint32 // atomic
	mu          sync.Mutex
}

// Pool - sharded for better concurrency
type Pool struct {
	shards     []*Shard
	shardCount int
	shardMask  uint32

	// Fast path cache (like sync.Pool)
	fastCache sync.Pool

	// Ring buffer for bulk operations
	ringBuffer *RingBuffer

	// Statistics (optional, can be disabled)
	stats      *PoolStats
	trackStats bool
}

// PoolStats - all atomic for lock-free updates
type PoolStats struct {
	Gets           uint64
	Puts           uint64
	Hits           uint64
	Misses         uint64
	Evictions      uint64
	ClockSweeps    uint64
	DirtyBuffers   uint64
	RingBufferUses uint64
}

// RingBuffer - optimized with atomic operations
type RingBuffer struct {
	buffers []*Buffer
	size    int
	current uint32 // atomic
}

// Config for pool initialization
type Config struct {
	PoolSize          int
	ShardCount        int // New: number of shards
	EnableRingBuffer  bool
	RingBufferSize    int
	DefaultBufferSize int
	TrackStats        bool // New: disable for max performance
	EnableFastCache   bool // New: sync.Pool-like fast path
}

// DefaultConfig returns optimized defaults
func DefaultConfig() Config {
	return Config{
		PoolSize:          DefaultPoolSize,
		ShardCount:        DefaultShardCount,
		EnableRingBuffer:  true,
		RingBufferSize:    DefaultRingBufferSize,
		DefaultBufferSize: 4096,
		TrackStats:        true,
		EnableFastCache:   true,
	}
}

// NewPool creates an optimized sharded pool
func NewPool(config Config) *Pool {
	if config.PoolSize == 0 {
		config.PoolSize = DefaultPoolSize
	}
	if config.ShardCount == 0 {
		config.ShardCount = DefaultShardCount
	}
	if config.DefaultBufferSize == 0 {
		config.DefaultBufferSize = 4096
	}

	// Ensure shard count is power of 2 for fast masking
	shardCount := nextPowerOf2(config.ShardCount)
	buffersPerShard := config.PoolSize / shardCount

	pool := &Pool{
		shards:     make([]*Shard, shardCount),
		shardCount: shardCount,
		shardMask:  uint32(shardCount - 1),
		trackStats: config.TrackStats,
	}

	if config.TrackStats {
		pool.stats = &PoolStats{}
	}

	// Initialize shards
	for i := 0; i < shardCount; i++ {
		shard := &Shard{
			buffers:     make([]*Buffer, buffersPerShard),
			descriptors: make([]*BufferDescriptor, buffersPerShard),
		}

		for j := 0; j < buffersPerShard; j++ {
			shard.descriptors[j] = &BufferDescriptor{}
			shard.buffers[j] = &Buffer{
				B:    make([]byte, 0, config.DefaultBufferSize),
				desc: shard.descriptors[j],
				pool: pool,
			}
		}

		pool.shards[i] = shard
	}

	// Initialize fast cache if enabled
	if config.EnableFastCache {
		pool.fastCache.New = func() interface{} {
			return &Buffer{
				B:    make([]byte, 0, config.DefaultBufferSize),
				desc: &BufferDescriptor{},
				pool: pool,
			}
		}
	}

	// Initialize ring buffer
	if config.EnableRingBuffer {
		pool.ringBuffer = &RingBuffer{
			buffers: make([]*Buffer, config.RingBufferSize),
			size:    config.RingBufferSize,
		}
		for i := 0; i < config.RingBufferSize; i++ {
			pool.ringBuffer.buffers[i] = &Buffer{
				B:    make([]byte, 0, config.DefaultBufferSize),
				desc: &BufferDescriptor{},
				pool: pool,
			}
		}
	}

	return pool
}

// Get - optimized with fast path
func (p *Pool) Get() *Buffer {
	if p.trackStats {
		atomic.AddUint64(&p.stats.Gets, 1)
	}

	// Fast path: try lock-free cache first
	if p.fastCache.New != nil {
		if buf := p.fastCache.Get().(*Buffer); buf != nil {
			buf.B = buf.B[:0]
			atomic.StoreUint32(&buf.desc.state, uint32(StateClean))
			atomic.StoreUint32(&buf.desc.pinCount, 1)
			atomic.AddUint32(&buf.desc.usageCount, 1)
			if atomic.LoadUint32(&buf.desc.usageCount) > MaxUsageCount {
				atomic.StoreUint32(&buf.desc.usageCount, MaxUsageCount)
			}
			atomic.StoreInt64(&buf.desc.lastUsed, time.Now().UnixNano())

			if p.trackStats {
				atomic.AddUint64(&p.stats.Hits, 1)
			}
			return buf
		}
	}

	// Slow path: get from sharded pool
	return p.getFromShard()
}

// getFromShard - optimized shard selection
func (p *Pool) getFromShard() *Buffer {
	// Use goroutine ID approximation for shard selection
	shardIdx := fastRand() & p.shardMask
	shard := p.shards[shardIdx]

	shard.mu.Lock()
	defer shard.mu.Unlock()

	// Fast scan for unpinned clean buffer
	for _, buf := range shard.buffers {
		pinCount := atomic.LoadUint32(&buf.desc.pinCount)
		state := atomic.LoadUint32(&buf.desc.state)
		valid := atomic.LoadUint32(&buf.desc.valid)

		if pinCount == 0 && state == uint32(StateClean) && valid == 1 {
			atomic.StoreUint32(&buf.desc.pinCount, 1)

			usage := atomic.AddUint32(&buf.desc.usageCount, 1)
			if usage > MaxUsageCount {
				atomic.StoreUint32(&buf.desc.usageCount, MaxUsageCount)
			}

			atomic.StoreInt64(&buf.desc.lastUsed, time.Now().UnixNano())
			atomic.AddUint64(&buf.desc.totalUses, 1)

			if p.trackStats {
				atomic.AddUint64(&p.stats.Hits, 1)
			}
			return buf
		}
	}

	// Need eviction - use clock sweep
	if p.trackStats {
		atomic.AddUint64(&p.stats.Misses, 1)
	}

	victimIdx := p.clockSweepShard(shard)
	buf := shard.buffers[victimIdx]

	// Handle dirty buffer
	if atomic.LoadUint32(&buf.desc.state) == uint32(StateDirty) {
		if p.trackStats {
			atomic.AddUint64(&p.stats.DirtyBuffers, 1)
		}
	}

	// Reset buffer
	buf.B = buf.B[:0]
	atomic.StoreUint32(&buf.desc.pinCount, 1)
	atomic.StoreUint32(&buf.desc.usageCount, 1)
	atomic.StoreUint32(&buf.desc.state, uint32(StateClean))
	atomic.StoreUint32(&buf.desc.valid, 1)
	atomic.StoreInt64(&buf.desc.lastUsed, time.Now().UnixNano())
	atomic.AddUint64(&buf.desc.totalUses, 1)

	return buf
}

// clockSweepShard - optimized clock sweep on single shard
func (p *Pool) clockSweepShard(shard *Shard) int {
	if p.trackStats {
		atomic.AddUint64(&p.stats.ClockSweeps, 1)
	}

	size := len(shard.buffers)
	maxSweeps := size * 2

	for i := 0; i < maxSweeps; i++ {
		hand := atomic.AddUint32(&shard.clockHand, 1) % uint32(size)
		desc := shard.descriptors[hand]

		pinCount := atomic.LoadUint32(&desc.pinCount)
		if pinCount > 0 {
			continue
		}

		usageCount := atomic.LoadUint32(&desc.usageCount)
		if usageCount > 0 {
			atomic.AddUint32(&desc.usageCount, ^uint32(0)) // decrement
			continue
		}

		if p.trackStats {
			atomic.AddUint64(&p.stats.Evictions, 1)
		}
		return int(hand)
	}

	// Fallback: return first unpinned
	for i := range shard.buffers {
		if atomic.LoadUint32(&shard.descriptors[i].pinCount) == 0 {
			return i
		}
	}
	return 0
}

// Put - optimized with fast path
func (p *Pool) Put(buf *Buffer) {
	if buf == nil {
		return
	}

	if p.trackStats {
		atomic.AddUint64(&p.stats.Puts, 1)
	}

	// Decrement pin count
	pinCount := atomic.AddUint32(&buf.desc.pinCount, ^uint32(0)) // decrement

	// Fast path: return to sync.Pool cache
	if p.fastCache.New != nil && pinCount == 0 {
		buf.B = buf.B[:0]
		atomic.StoreUint32(&buf.desc.state, uint32(StateClean))
		p.fastCache.Put(buf)
		return
	}

	// Buffer stays in shard, just unpinned
}

// GetCold - optimized ring buffer with atomic operations
func (p *Pool) GetCold() *Buffer {
	if p.ringBuffer == nil {
		return p.Get()
	}

	if p.trackStats {
		atomic.AddUint64(&p.stats.RingBufferUses, 1)
	}

	idx := atomic.AddUint32(&p.ringBuffer.current, 1) % uint32(p.ringBuffer.size)
	buf := p.ringBuffer.buffers[idx]

	buf.B = buf.B[:0]
	atomic.StoreUint32(&buf.desc.state, uint32(StateClean))
	atomic.StoreUint32(&buf.desc.valid, 1)
	atomic.StoreInt64(&buf.desc.lastUsed, time.Now().UnixNano())

	return buf
}

// Pin - atomic increment
func (p *Pool) Pin(buf *Buffer) {
	atomic.AddUint32(&buf.desc.pinCount, 1)
}

// Unpin - atomic decrement
func (p *Pool) Unpin(buf *Buffer) {
	if atomic.LoadUint32(&buf.desc.pinCount) > 0 {
		atomic.AddUint32(&buf.desc.pinCount, ^uint32(0))
	}
}

// MarkDirty - atomic state update
func (p *Pool) MarkDirty(buf *Buffer) {
	atomic.StoreUint32(&buf.desc.state, uint32(StateDirty))
}

// GetStats - returns copy of stats
func (p *Pool) GetStats() PoolStats {
	if !p.trackStats || p.stats == nil {
		return PoolStats{}
	}

	return PoolStats{
		Gets:           atomic.LoadUint64(&p.stats.Gets),
		Puts:           atomic.LoadUint64(&p.stats.Puts),
		Hits:           atomic.LoadUint64(&p.stats.Hits),
		Misses:         atomic.LoadUint64(&p.stats.Misses),
		Evictions:      atomic.LoadUint64(&p.stats.Evictions),
		ClockSweeps:    atomic.LoadUint64(&p.stats.ClockSweeps),
		DirtyBuffers:   atomic.LoadUint64(&p.stats.DirtyBuffers),
		RingBufferUses: atomic.LoadUint64(&p.stats.RingBufferUses),
	}
}

// Buffer methods (optimized)

func (b *Buffer) Write(p []byte) (int, error) {
	b.B = append(b.B, p...)
	atomic.StoreUint32(&b.desc.state, uint32(StateDirty))
	return len(p), nil
}

func (b *Buffer) WriteString(s string) (int, error) {
	b.B = append(b.B, s...)
	atomic.StoreUint32(&b.desc.state, uint32(StateDirty))
	return len(s), nil
}

func (b *Buffer) WriteByte(c byte) error {
	b.B = append(b.B, c)
	atomic.StoreUint32(&b.desc.state, uint32(StateDirty))
	return nil
}

func (b *Buffer) Bytes() []byte  { return b.B }
func (b *Buffer) String() string { return string(b.B) }
func (b *Buffer) Len() int       { return len(b.B) }

func (b *Buffer) Reset() {
	b.B = b.B[:0]
	atomic.StoreUint32(&b.desc.state, uint32(StateClean))
}

func (b *Buffer) ReadFrom(r io.Reader) (int64, error) {
	n := int64(len(b.B))
	for {
		if len(b.B) == cap(b.B) {
			newBuf := make([]byte, len(b.B), 2*cap(b.B)+1)
			copy(newBuf, b.B)
			b.B = newBuf
		}
		m, err := r.Read(b.B[len(b.B):cap(b.B)])
		b.B = b.B[:len(b.B)+m]
		n += int64(m)
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			if m > 0 {
				atomic.StoreUint32(&b.desc.state, uint32(StateDirty))
			}
			return n - int64(len(b.B)), err
		}
	}
}

func (b *Buffer) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(b.B)
	return int64(n), err
}

func (b *Buffer) IsDirty() bool {
	return atomic.LoadUint32(&b.desc.state) == uint32(StateDirty)
}

func (b *Buffer) UsageCount() int32 {
	return int32(atomic.LoadUint32(&b.desc.usageCount))
}

func (b *Buffer) PinCount() int32 {
	return int32(atomic.LoadUint32(&b.desc.pinCount))
}

// Utility functions

// nextPowerOf2 returns the next power of 2 >= n
func nextPowerOf2(n int) int {
	if n <= 0 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n++
	return n
}

// fastRand - fast random number for shard selection
var rngSeed uint32 = 1

func fastRand() uint32 {
	// XorShift32
	seed := atomic.LoadUint32(&rngSeed)
	seed ^= seed << 13
	seed ^= seed >> 17
	seed ^= seed << 5
	atomic.StoreUint32(&rngSeed, seed)
	return seed
}

// Default pool
var defaultPool = NewPool(DefaultConfig())

func Get() *Buffer        { return defaultPool.Get() }
func Put(buf *Buffer)     { defaultPool.Put(buf) }
func GetCold() *Buffer    { return defaultPool.GetCold() }
func GetStats() PoolStats { return defaultPool.GetStats() }
