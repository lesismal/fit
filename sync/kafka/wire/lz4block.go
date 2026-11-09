package kafkawire

// LZ4Block appends src to dst as one raw LZ4 block, with no frame around
// it, as Pulsar's LZ4 compression writes a message: the reader is told the
// size it decompresses to apart.
func LZ4Block(dst, src []byte) []byte {
	var table [1 << lz4HashLog]int32
	return lz4CompressBlock(dst, src, &table)
}

// LZ4Unblock appends the content of a raw LZ4 block to dst, failing past
// limit bytes of dst in all.
func LZ4Unblock(dst, src []byte, limit int) ([]byte, error) {
	return lz4DecompressBlock(dst, src, limit)
}
