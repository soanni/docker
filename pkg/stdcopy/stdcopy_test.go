package stdcopy

import (
	"bytes"
	"io"
	"io/ioutil"
	"strings"
	"testing"
	"time"

	"github.com/Sirupsen/logrus"
)

func TestNewStdWriter(t *testing.T) {
	writer := NewStdWriter(ioutil.Discard, Stdout)
	if writer == nil {
		t.Fatalf("NewStdWriter with an invalid StdType should not return nil.")
	}
}

func TestWriteWithUnitializedStdWriter(t *testing.T) {
	writer := StdWriter{
		Writer:  nil,
		prefix:  Stdout,
		sizeBuf: make([]byte, 4),
	}
	n, err := writer.Write([]byte("Something here"))
	if n != 0 || err == nil {
		t.Fatalf("Should fail when given an uncomplete or uninitialized StdWriter")
	}
}

func TestWriteWithNilBytes(t *testing.T) {
	writer := NewStdWriter(ioutil.Discard, Stdout)
	n, err := writer.Write(nil)
	if err != nil {
		t.Fatalf("Shouldn't have fail when given no data")
	}
	if n > 0 {
		t.Fatalf("Write should have written 0 byte, but has written %d", n)
	}
}

func TestWrite(t *testing.T) {
	writer := NewStdWriter(ioutil.Discard, Stdout)
	data := []byte("Test StdWrite.Write")
	n, err := writer.Write(data)
	if err != nil {
		t.Fatalf("Error while writing with StdWrite")
	}
	if n != len(data) {
		t.Fatalf("Write should have writen %d byte but wrote %d.", len(data), n)
	}
}

func TestStdCopyWithInvalidInputHeader(t *testing.T) {
	dstOut := NewStdWriter(ioutil.Discard, Stdout)
	dstErr := NewStdWriter(ioutil.Discard, Stderr)
	src := strings.NewReader("Invalid input")
	_, err := StdCopy(dstOut, dstErr, src)
	if err == nil {
		t.Fatal("StdCopy with invalid input header should fail.")
	}
}

func TestStdCopyWithCorruptedPrefix(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03}
	src := bytes.NewReader(data)
	written, err := StdCopy(nil, nil, src)
	if err != nil {
		t.Fatalf("StdCopy should not return an error with corrupted prefix.")
	}
	if written != 0 {
		t.Fatalf("StdCopy should have written 0, but has written %d", written)
	}
}

func TestStdCopy(t *testing.T) {
	logrus.Infof("stcopy....")
	reader, writer := io.Pipe()
	dstOut := NewStdWriter(ioutil.Discard, Stdout)
	dstErr := NewStdWriter(ioutil.Discard, Stderr)

	done := make(chan bool)
	writtenChannel := make(chan int64)

	go func() {
		written, err := StdCopy(dstOut, dstErr, reader)
		if err != nil {
			t.Fatal(err)
		}
		logrus.Infof("written %v", written)
		writtenChannel <- written
	}()

	go func() {
		for i := 0; i < 2; i++ {
			writer.Write(makeBytes(0x01, 2*16908546))
			time.Sleep(100 * time.Millisecond)
		}
		writer.Close()
		done <- true
	}()

	<-done
	written := <-writtenChannel

	if written != 0 {
		logrus.Infof("hum.. %v", written)
	}
}

func makeBytes(b byte, length int) []byte {
	bytes := make([]byte, length)

	bytes[0] = b
	for i := 1; i < length; i++ {
		bytes[i] = 0x02
	}

	return bytes
}

func BenchmarkWrite(b *testing.B) {
	w := NewStdWriter(ioutil.Discard, Stdout)
	data := []byte("Test line for testing stdwriter performance\n")
	data = bytes.Repeat(data, 100)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := w.Write(data); err != nil {
			b.Fatal(err)
		}
	}
}
