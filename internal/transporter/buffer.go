package transporter

import (
	"errors"
	"io"
	"net"
	"syscall"
	"time"

	"go.uber.org/atomic"

	"github.com/Ehco1996/ehco/internal/constant"
	"github.com/Ehco1996/ehco/internal/logger"
	"github.com/Ehco1996/ehco/internal/web"
)

// 全局pool
var BufferPool *BytePool

func init() {
	BufferPool = NewBytePool(constant.BUFFER_POOL_SIZE, constant.BUFFER_SIZE)
}

// BytePool implements a leaky pool of []byte in the form of a bounded channel
type BytePool struct {
	c    chan []byte
	size int
}

// NewBytePool creates a new BytePool bounded to the given maxSize, with new
// byte arrays sized based on width.
func NewBytePool(maxSize int, size int) (bp *BytePool) {
	return &BytePool{
		c:    make(chan []byte, maxSize),
		size: size,
	}
}

// Get gets a []byte from the BytePool, or creates a new one if none are
// available in the pool.
func (bp *BytePool) Get() (b []byte) {
	select {
	case b = <-bp.c:
	// reuse existing buffer
	default:
		// create new buffer
		b = make([]byte, bp.size)
	}
	return
}

// Put returns the given Buffer to the BytePool.
func (bp *BytePool) Put(b []byte) {
	select {
	case bp.c <- b:
		// buffer went back into pool
	default:
		// buffer didn't go back into pool, just discard
	}
}

type ReadOnlyReader struct {
	io.Reader
}

type WriteOnlyWriter struct {
	io.Writer
}

// mute broken pipe or connection reset err.
func ErrCanMute(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || err == nil
}

func transport(conn1, conn2 net.Conn, remote string) error {
	// conn1 to conn2
	go func() {
		rn, err := io.Copy(WriteOnlyWriter{Writer: conn1}, ReadOnlyReader{Reader: conn2})
		web.NetWorkTransmitBytes.WithLabelValues(remote, web.METRIC_CONN_TCP).Add(float64(rn * 2))
		if !ErrCanMute(err) {
			logger.Errorf("err in transport 1 err:%s", err)
		}
		conn1.SetReadDeadline(time.Now().Add(constant.IdleTimeOut))
	}()

	// conn2 to conn1
	rn, err := io.Copy(WriteOnlyWriter{Writer: conn2}, ReadOnlyReader{Reader: conn1})
	web.NetWorkTransmitBytes.WithLabelValues(remote, web.METRIC_CONN_TCP).Add(float64(rn * 2))
	if ErrCanMute(err) {
		err = nil
	}
	conn2.SetReadDeadline(time.Now().Add(constant.IdleTimeOut))
	time.Sleep(constant.IdleTimeOut)
	return err
}

type BufferCh struct {
	Ch      chan []byte
	Handled atomic.Bool
	UDPAddr *net.UDPAddr
}

func newudpBufferCh(clientUDPAddr *net.UDPAddr) *BufferCh {
	return &BufferCh{
		Ch:      make(chan []byte, 100),
		Handled: atomic.Bool{},
		UDPAddr: clientUDPAddr,
	}
}
