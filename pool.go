package gohotpool

import (
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"
)

const (
	MaxUsageCount         = 5
	DefaultPoolSize       = 1024
	DefaultRingBufferSize = 32
	DefaultShardCount     = 16
)

// BufferState represents the state of a buffer
type BufferState uint32

const (
	StateClean BufferState = iota
	StateDirty
	StateWriting
)

// BufferDescriptor optimized with atomic operations instead of mutex
type BufferDescriptor struct {
	usageCount uint32 // atomic, 0-5
	pinCount   uint32 // atomic
	state      uint32 // atomic BufferState
	_          uint32 // padding for alignment
}

// Buffer represents a byte buffer with metadata
type Buffer struct {
	B    []byte
	desc *BufferDescriptor
	pool *Pool
}

type perPCache struct {
	buf unsafe.Pointer
	_   [56]byte // padding to avoid false sharing
}

// Shard each shard has its own lock to reduce contention
type Shard struct {
	buffers     []*Buffer
	descriptors []*BufferDescriptor
	clockHand   uint32 // atomic
	mu          sync.Mutex
	_           [40]byte
}

// Pool sharded for better concurrency
type Pool struct {
	perP           []perPCache
	numProcs       int
	shards         []*Shard
	shardCount     int
	shardMask      uint32
	overflow       sync.Pool
	ringBuffer     *RingBuffer
	stats          *PoolStats // disabled by default for performance
	trackStats     bool
	defaultBufSize int
}

// PoolStats all atomic for lock-free updates
type PoolStats struct {
	Gets           uint64
	Puts           uint64
	Hits           uint64
	Misses         uint64
	Evictions      uint64
	ClockSweeps    uint64
	DirtyBuffers   uint64
	RingBufferUses uint64
	PerPHits       uint64
}

// RingBuffer optimized with atomic operations
type RingBuffer struct {
	buffers []*Buffer
	size    int
	current uint32 // atomic
}

// Config for pool initialization
type Config struct {
	PoolSize          int
	ShardCount        int
	EnableRingBuffer  bool
	RingBufferSize    int
	DefaultBufferSize int
	TrackStats        bool // false by default for max performance
}

// DefaultConfig returns optimized defaults
func DefaultConfig() Config {
	return Config{
		PoolSize:          DefaultPoolSize,
		ShardCount:        DefaultShardCount,
		EnableRingBuffer:  true,
		RingBufferSize:    DefaultRingBufferSize,
		DefaultBufferSize: 4096,
		TrackStats:        false,
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
	if buffersPerShard < 1 {
		buffersPerShard = 1
	}
	numProcs := runtime.GOMAXPROCS(0)

	pool := &Pool{
		perP:           make([]perPCache, numProcs),
		numProcs:       numProcs,
		shards:         make([]*Shard, shardCount),
		shardCount:     shardCount,
		shardMask:      uint32(shardCount - 1),
		trackStats:     config.TrackStats,
		defaultBufSize: config.DefaultBufferSize,
	}

	if config.TrackStats {
		pool.stats = &PoolStats{}
	}

	for i := 0; i < numProcs; i++ {
		buf := pool.newBuffer()
		atomic.StorePointer(&pool.perP[i].buf, unsafe.Pointer(buf))
	}
	// Initialize shards
	for i := 0; i < shardCount; i++ {
		shard := &Shard{
			buffers:     make([]*Buffer, buffersPerShard),
			descriptors: make([]*BufferDescriptor, buffersPerShard),
		}

		for j := 0; j < buffersPerShard; j++ {
			shard.descriptors[j] = &BufferDescriptor{
				usageCount: 1,
			}
			shard.buffers[j] = &Buffer{
				B:    make([]byte, 0, config.DefaultBufferSize),
				desc: shard.descriptors[j],
				pool: pool,
			}
		}

		pool.shards[i] = shard
	}

	pool.overflow.New = func() interface{} {
		return pool.newBuffer()
	}

	// Initialize ring buffer
	if config.EnableRingBuffer {
		pool.ringBuffer = &RingBuffer{
			buffers: make([]*Buffer, config.RingBufferSize),
			size:    config.RingBufferSize,
		}
		for i := 0; i < config.RingBufferSize; i++ {
			pool.ringBuffer.buffers[i] = pool.newBuffer()
		}
	}

	return pool
}

func (p *Pool) newBuffer() *Buffer {
	return &Buffer{
		B:    make([]byte, 0, p.defaultBufSize),
		desc: &BufferDescriptor{usageCount: 1},
		pool: p,
	}
}

// Get optimized with per-P fast path using atomic swap
func (p *Pool) Get() *Buffer {

	if p.trackStats {
		atomic.AddUint64(&p.stats.Gets, 1)
	}
	// Try per-P cache first
	pid := runtime_procPin()
	if pid < p.numProcs {

		ptr := atomic.SwapPointer(&p.perP[pid].buf, nil)
		runtime_procUnpin()
		if ptr != nil {
			buf := (*Buffer)(ptr)
			buf.B = buf.B[:0]
			atomic.StoreUint32(&buf.desc.state, uint32(StateClean))
			atomic.StoreUint32(&buf.desc.pinCount, 1)

			if p.trackStats {
				atomic.AddUint64(&p.stats.PerPHits, 1)
				atomic.AddUint64(&p.stats.Hits, 1)
			}
			return buf
		}
	} else {
		runtime_procUnpin()
	}

	if iface := p.overflow.Get(); iface != nil {
		b := iface.(*Buffer)
		b.B = b.B[:0]
		atomic.StoreUint32(&b.desc.state, uint32(StateClean))
		atomic.StoreUint32(&b.desc.pinCount, 1)

		if p.trackStats {
			atomic.AddUint64(&p.stats.Hits, 1)
		}
		return b
	}
	// Slow path: get from sharded pool
	return p.getFromShard()
}

// getFromShard - optimized shard selection
func (p *Pool) getFromShard() *Buffer {
	// Use fast random for shard selection
	shardIdx := fastRand() & p.shardMask
	shard := p.shards[shardIdx]

	shard.mu.Lock()

	// Fast scan for unpinned clean buffer
	for _, buf := range shard.buffers {

		if buf.desc.pinCount == 0 && buf.desc.state == uint32(StateClean) {
			buf.desc.pinCount = 1
			buf.desc.usageCount++
			if buf.desc.usageCount > MaxUsageCount {
				buf.desc.usageCount = MaxUsageCount
			}
			shard.mu.Unlock()
			buf.B = buf.B[:0]

			if p.trackStats {
				atomic.AddUint64(&p.stats.Hits, 1)
			}

			return buf
		}
	}

	if p.trackStats {
		atomic.AddUint64(&p.stats.Misses, 1)
	}

	victimIdx := p.clockSweepShard(shard)
	buf := shard.buffers[victimIdx]

	// Handle dirty buffer
	if buf.desc.state == uint32(StateDirty) {
		if p.trackStats {
			atomic.AddUint64(&p.stats.DirtyBuffers, 1)
		}
	}

	// Reset buffer (direct access under mutex)
	buf.B = buf.B[:0]
	buf.desc.pinCount = 1
	buf.desc.usageCount = 1
	buf.desc.state = uint32(StateClean)

	shard.mu.Unlock()
	return buf
}

// clockSweepShard  optimized clock sweep on single shard (called with mutex held)
func (p *Pool) clockSweepShard(shard *Shard) int {
	if p.trackStats {
		atomic.AddUint64(&p.stats.ClockSweeps, 1)
	}

	size := len(shard.buffers)
	if size == 0 {
		return 0
	}
	maxSweeps := size * 2

	for i := 0; i < maxSweeps; i++ {
		//atomic increment for clock hand (can be accessed during stats)
		hand := atomic.AddUint32(&shard.clockHand, 1) % uint32(size)
		desc := shard.descriptors[hand]

		if desc.pinCount > 0 {
			continue
		}

		if desc.usageCount > 0 {
			desc.usageCount--
			continue
		}

		if p.trackStats {
			atomic.AddUint64(&p.stats.Evictions, 1)
		}
		return int(hand)
	}

	// Fallback: return first unpinned
	for i := range shard.buffers {
		if shard.descriptors[i].pinCount == 0 {
			return i
		}
	}
	return 0
}

// Put  optimized with per-P fast path using atomic compare-and-swap
func (p *Pool) Put(buf *Buffer) {
	if buf == nil {
		return
	}

	if p.trackStats {
		atomic.AddUint64(&p.stats.Puts, 1)
	}

	// Decrement pin count
	newPinCount := atomic.AddUint32(&buf.desc.pinCount, ^uint32(0)) // decrement
	if newPinCount != 0 {
		// Still pinned, cannot return to pool
		return
	}

	buf.B = buf.B[:0]
	atomic.StoreUint32(&buf.desc.state, uint32(StateClean))

	// Try to store in per-P cache
	pid := runtime_procPin()
	if pid < p.numProcs {
		if atomic.CompareAndSwapPointer(&p.perP[pid].buf, nil, unsafe.Pointer(buf)) {
			runtime_procUnpin()
			return
		}
	}
	runtime_procUnpin()

	p.overflow.Put(buf)
}

// GetCold  optimized ring buffer with atomic operations
func (p *Pool) GetCold() *Buffer {
	if p.ringBuffer == nil {
		return p.Get()
	}

	if p.trackStats {
		atomic.AddUint64(&p.stats.RingBufferUses, 1)
	}

	for attempts := 0; attempts < p.ringBuffer.size*2; attempts++ {
		idx := atomic.AddUint32(&p.ringBuffer.current, 1) % uint32(p.ringBuffer.size)
		buf := p.ringBuffer.buffers[idx]

		if atomic.CompareAndSwapUint32(&buf.desc.pinCount, 0, 1) {
			buf.B = buf.B[:0]
			atomic.StoreUint32(&buf.desc.state, uint32(StateClean))
			return buf
		}
	}
	//all buffer slots are in use, fallback to regular Get
	return p.Get()
}

func (p *Pool) PutCold(buf *Buffer) {
	if buf == nil {
		return
	}
	buf.B = buf.B[:0]
	atomic.StoreUint32(&buf.desc.state, uint32(StateClean))
	atomic.StoreUint32(&buf.desc.pinCount, 0) // unpin

}

// Pin atomic increment
func (p *Pool) Pin(buf *Buffer) {
	atomic.AddUint32(&buf.desc.pinCount, 1)
}

// Unpin atomic decrement
func (p *Pool) Unpin(buf *Buffer) {
	if atomic.LoadUint32(&buf.desc.pinCount) > 0 {
		atomic.AddUint32(&buf.desc.pinCount, ^uint32(0))
	}
}

// MarkDirty atomic state update
func (p *Pool) MarkDirty(buf *Buffer) {
	atomic.StoreUint32(&buf.desc.state, uint32(StateDirty))
}

// GetStats returns copy of stats
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
		PerPHits:       atomic.LoadUint64(&p.stats.PerPHits),
	}
}

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
func (b *Buffer) Cap() int       { return cap(b.B) }

func (b *Buffer) Reset() {
	b.B = b.B[:0]
	atomic.StoreUint32(&b.desc.state, uint32(StateClean))
}

func (b *Buffer) ReadFrom(r io.Reader) (int64, error) {
	startLen := len(b.B)
	for {
		if len(b.B) == cap(b.B) {
			newCap := cap(b.B)*2 + 512
			newBuf := make([]byte, len(b.B), newCap)
			copy(newBuf, b.B)
			b.B = newBuf
		}
		m, err := r.Read(b.B[len(b.B):cap(b.B)])
		b.B = b.B[:len(b.B)+m]

		if err != nil {
			if err == io.EOF {
				err = nil
			}
			if len(b.B) > startLen {
				atomic.StoreUint32(&b.desc.state, uint32(StateDirty))
			}
			return int64(len(b.B) - startLen), err
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

// Grow ensures buffer has at least n bytes of capacity
func (b *Buffer) Grow(n int) {
	if cap(b.B)-len(b.B) >= n {
		return
	}
	newCap := len(b.B) + n
	newBuf := make([]byte, len(b.B), newCap)
	copy(newBuf, b.B)
	b.B = newBuf
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

var rngState uint32 = 1

func fastRand() uint32 {
	s := atomic.AddUint32(&rngState, 0x9E3779B9) // golden ratio
	s ^= s >> 16
	s *= 0x85ebca6b
	s ^= s >> 13
	s *= 0xc2b2ae35
	s ^= s >> 16

	return s
}

//go:linkname runtime_procPin runtime.procPin
func runtime_procPin() int

//go:linkname runtime_procUnpin runtime.procUnpin
func runtime_procUnpin()

// Default pool
var defaultPool = NewPool(DefaultConfig())

func Get() *Buffer          { return defaultPool.Get() }
func Put(buf *Buffer)       { defaultPool.Put(buf) }
func GetCold() *Buffer      { return defaultPool.GetCold() }
func PutCold(buf *Buffer)   { defaultPool.PutCold(buf) }
func GetStats() PoolStats   { return defaultPool.GetStats() }
func Pin(buf *Buffer)       { defaultPool.Pin(buf) }
func Unpin(buf *Buffer)     { defaultPool.Unpin(buf) }
func MarkDirty(buf *Buffer) { defaultPool.MarkDirty(buf) }
