package xnote

import (
	"bufio"
	"testing"
)

type transferCountingWriter struct {
	bytes, calls int64
}

func (w *transferCountingWriter) Write(p []byte) (int, error) {
	w.bytes += int64(len(p))
	w.calls++
	return len(p), nil
}

// BenchmarkBLEReceiveDrain measures host-side copying, queueing and buffered
// writes for the observed 244-byte notification size. Its sink deliberately
// excludes filesystem and Bluetooth costs; this is not a radio speed benchmark.
// Draining each callback keeps the queue below capacity, as at the measured
// recorder rate (~250 notifications per second). Overflow behavior is covered
// separately by TestAudioOverflowPreservesContiguousPrefix.
func BenchmarkBLEReceiveDrain(b *testing.B) {
	packet := make([]byte, 244)
	queue := &audioQueue{packets: make(chan []byte, 4096), fail: make(chan error, 1)}
	sink := new(transferCountingWriter)
	writer := bufio.NewWriterSize(sink, 64*1024)
	b.SetBytes(int64(len(packet)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		queue.receive(packet)
		if _, err := writer.Write(<-queue.packets); err != nil {
			b.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		b.Fatal(err)
	}
	b.StopTimer()
	if sink.bytes != int64(b.N)*int64(len(packet)) || len(queue.fail) != 0 {
		b.Fatalf("received %d bytes, want %d; failures=%d", sink.bytes, int64(b.N)*int64(len(packet)), len(queue.fail))
	}
	b.ReportMetric(float64(sink.calls)/float64(b.N), "writes/packet")
}
