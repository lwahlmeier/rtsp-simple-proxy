package main

import (
	"net"
	"sync"
	"time"
)

type streamUdpListenerState int

var _RTPPing = []byte{0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
var _RTCPPing = []byte{0x80, 0xc9, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00}

const (
	_UDPL_STATE_STARTING streamUdpListenerState = iota
	_UDPL_STATE_RUNNING
)

type streamUdpListener struct {
	p             *program
	nconn         *net.UDPConn
	state         streamUdpListenerState
	done          chan struct{}
	publisherIp   net.IP
	publisherPort int
	trackId       int
	flow          trackFlow
	path          string
	mutex         sync.Mutex
	lastFrameTime time.Time
}

func newStreamUdpListener(p *program, port int) (*streamUdpListener, error) {
	nconn, err := net.ListenUDP("udp", &net.UDPAddr{
		Port: port,
	})
	if err != nil {
		return nil, err
	}

	l := &streamUdpListener{
		p:     p,
		nconn: nconn,
		state: _UDPL_STATE_STARTING,
		done:  make(chan struct{}),
	}

	return l, nil
}

func (l *streamUdpListener) close() {
	select {
	case <-l.done:
		return
	default:
		close(l.done)
	}
	l.nconn.Close()
}

func (l *streamUdpListener) start() {
	l.state = _UDPL_STATE_RUNNING
	go l.run()
}

func (l *streamUdpListener) sendNATPing() {
	dstAddr := &net.UDPAddr{
		Port: l.publisherPort,
		IP:   l.publisherIp,
	}
	if l.flow == _TRACK_FLOW_RTP {
		l.nconn.WriteTo(_RTPPing, dstAddr)
	} else if l.flow == _TRACK_FLOW_RTCP {
		l.nconn.WriteTo(_RTCPPing, dstAddr)
	}
}

func (l *streamUdpListener) run() {
	l.sendNATPing()
	lastNatTime := time.Now()
	buf := make([]byte, 1024*64)

	for {

		n, addr, err := l.nconn.ReadFromUDP(buf)
		if err != nil {
			break
		}

		if !l.publisherIp.Equal(addr.IP) || addr.Port != l.publisherPort {
			continue
		}

		l.p.tcpl.forwardTrack(l.path, l.trackId, l.flow, buf[:n])
		// a new buffer slice is needed for each read.
		// this is necessary since the buffer is propagated with channels
		// so it must be unique.
		buf = buf[n:]
		if len(buf) < 2048 {
			buf = make([]byte, 1024*64)
		}

		func() {
			l.mutex.Lock()
			defer l.mutex.Unlock()
			l.lastFrameTime = time.Now()
		}()
		if time.Since(lastNatTime) >= time.Duration(30*time.Second) {
			l.sendNATPing()
			lastNatTime = time.Now()
		}

	}

}
