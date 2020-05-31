package main

import (
	"net"

	"github.com/PremiereGlobal/stim/pkg/stimlog"
)

type udpWrite struct {
	addr *net.UDPAddr
	buf  []byte
}

type serverUdpListener struct {
	p     *program
	nconn *net.UDPConn
	flow  trackFlow
	write chan *udpWrite
	done  chan struct{}
	log   stimlog.StimLogger
}

func newServerUdpListener(p *program, port int, flow trackFlow) (*serverUdpListener, error) {
	nconn, err := net.ListenUDP("udp", &net.UDPAddr{
		Port: port,
	})
	if err != nil {
		return nil, err
	}

	var label string
	if flow == _TRACK_FLOW_RTP {
		label = "RTP"
	} else {
		label = "RTCP"
	}
	l := &serverUdpListener{
		p:     p,
		nconn: nconn,
		flow:  flow,
		write: make(chan *udpWrite, 100),
		done:  make(chan struct{}),
		log:   stimlog.GetLoggerWithPrefix("[UDP/" + label + " listener]"),
	}

	l.log.Info("opened on :{}", port)
	return l, nil
}

func (l *serverUdpListener) run() {
	go func() {
		for w := range l.write {
			l.nconn.WriteTo(w.buf, w.addr)
		}
	}()

	buf := make([]byte, 2048) // UDP MTU is 1400
	for {
		_, _, err := l.nconn.ReadFromUDP(buf)
		if err != nil {
			break
		}
	}

	l.close()
}

func (l *serverUdpListener) close() {
	select {
	case <-l.done:
		return
	default:
		close(l.done)
	}
	l.nconn.Close()
	close(l.write)

}
