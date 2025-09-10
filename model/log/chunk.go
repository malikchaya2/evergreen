/*
Log Chunk

A log chunk is a storage abstraction representing a sequential, continuous
section of a single log. Logs can be coherently represented by a set of ordered
chunks.
*/
package log

// chunkInfo represents a log chunk file's metadata that enables optimized
// fetching of log files stored as a set of chunks in pail-backed bucket
// storage.
type ChunkInfo struct {
	Key      string
	Sequence int
	Start    int64
	End      int64
	NumLines int
	Upload   int64
}

// chunkGroup represents a set of chunks belonging to a single log.
type ChunkGroup struct {
	Name   string
	Chunks []ChunkInfo
}
