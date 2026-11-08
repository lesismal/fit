package kafkawire

import (
	"cmp"
	"encoding/binary"
	"slices"
)

// PartitionFor returns the partition of key among n as Kafka's Java
// client's default partitioner places keyed records: murmur2 of the key,
// its sign bit cleared, modulo n. Records written by fit and by Java
// clients with the same key land together.
func PartitionFor(key []byte, n int32) int32 {
	if n <= 0 {
		return 0
	}
	return (murmur2(key) & 0x7fffffff) % n
}

// murmur2 is Kafka's murmur2 (org.apache.kafka.common.utils.Utils).
func murmur2(data []byte) int32 {
	const (
		seed uint32 = 0x9747b28c
		m    uint32 = 0x5bd1e995
		r           = 24
	)
	n := len(data)
	h := seed ^ uint32(n)
	for i := 0; i+4 <= n; i += 4 {
		k := binary.LittleEndian.Uint32(data[i:])
		k *= m
		k ^= k >> r
		k *= m
		h *= m
		h ^= k
	}
	tail := data[n&^3:]
	switch len(tail) {
	case 3:
		h ^= uint32(tail[2]) << 16
		fallthrough
	case 2:
		h ^= uint32(tail[1]) << 8
		fallthrough
	case 1:
		h ^= uint32(tail[0])
		h *= m
	}
	h ^= h >> 13
	h *= m
	h ^= h >> 15
	return int32(h)
}

// sortPartitions puts each topic's partitions in their numbers' order.
func sortPartitions(m *Metadata) {
	for i := range m.Topics {
		slices.SortFunc(m.Topics[i].Partitions, func(a, b PartitionMetadata) int { return cmp.Compare(a.Partition, b.Partition) })
	}
}
