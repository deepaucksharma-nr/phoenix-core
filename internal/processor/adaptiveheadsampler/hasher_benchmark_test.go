package adaptiveheadsampler

import (
	"crypto/sha1"
	"encoding/binary"
	"testing"

	"github.com/cespare/xxhash/v2"
)

func BenchmarkXORTraceIDHasher(b *testing.B) {
	// Create a processor instance
	p := &adaptiveHeadSamplerProcessor{}
	
	// Create a sample trace ID
	traceID := make([]byte, 16)
	for i := 0; i < 16; i++ {
		traceID[i] = byte(i)
	}
	
	// Run the benchmark
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.xorTraceIDHasher(traceID)
	}
}

func BenchmarkRecordIDHasher_xxHash(b *testing.B) {
	// Create a processor instance
	p := &adaptiveHeadSamplerProcessor{}
	
	// Create a sample record ID (for logs)
	recordID := []byte("test.container.id.with.long.name.and.some.more.data.to.hash")
	
	// Run the benchmark
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.recordIDHasher(recordID)
	}
}

// This benchmark is for comparing with the old SHA1 approach
func BenchmarkSHA1Hasher(b *testing.B) {
	// Create a sample record ID (for logs)
	recordID := []byte("test.container.id.with.long.name.and.some.more.data.to.hash")
	
	// Run the benchmark
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h := sha1.New()
		h.Write(recordID)
		sum := h.Sum(nil)
		_ = binary.BigEndian.Uint64(sum[0:8])
	}
}

// This benchmark is for comparing with xxHash directly
func BenchmarkXXHasher(b *testing.B) {
	// Create a sample record ID (for logs)
	recordID := []byte("test.container.id.with.long.name.and.some.more.data.to.hash")
	
	// Run the benchmark
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = xxhash.Sum64(recordID)
	}
}