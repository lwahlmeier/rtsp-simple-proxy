package main

import (
	"net"
	"sync"

	sets "github.com/deckarep/golang-set"

	"github.com/PremiereGlobal/stim/pkg/stimlog"
	"github.com/lwahlmeier/gortsplib"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var tcpConnections = promauto.NewCounter(prometheus.CounterOpts{
	Name: "tcp_connections_total",
	Help: "The total number tcp connections",
})

var skippedFrames = promauto.NewCounter(prometheus.CounterOpts{
	Name: "skipped_frames_total",
	Help: "The total of frames skipped",
})

type serverTcpListener struct {
	p     *program
	nconn *net.TCPListener
	mutex sync.RWMutex
	// clients map[*serverClient]struct{}
	clients sets.Set
	done    chan struct{}
	log     stimlog.StimLogger
}

func newServerTcpListener(p *program) (*serverTcpListener, error) {
	nconn, err := net.ListenTCP("tcp", &net.TCPAddr{
		Port: p.conf.Server.RtspPort,
	})
	if err != nil {
		return nil, err
	}

	l := &serverTcpListener{
		p:     p,
		nconn: nconn,
		// clients: make(map[*serverClient]struct{}),
		clients: sets.NewSet(),
		done:    make(chan struct{}),
		log:     stimlog.GetLoggerWithPrefix("[TCP listener]"),
	}

	l.log.Info("opened on :{}", p.conf.Server.RtspPort)
	return l, nil
}

func (l *serverTcpListener) addServerClient(sc *serverClient) {
	l.clients.Add(sc)
}
func (l *serverTcpListener) removeServerClient(sc *serverClient) {
	l.clients.Remove(sc)
}

func (l *serverTcpListener) closeClientsOnPath(path string) {
	l.clients.Each(func(i interface{}) bool {
		c := i.(*serverClient)
		_, _, _, cPath := c.GetClientInfo()
		if cPath == path {
			c.close()
		}
		return false
	})
}

func (l *serverTcpListener) run() {
	for {
		nconn, err := l.nconn.AcceptTCP()
		if err != nil {
			break
		}
		tcpConnections.Inc()
		nconn.SetNoDelay(true)
		newServerClient(l.p, nconn)

	}

	l.clients.Each(func(i interface{}) bool {
		c := i.(*serverClient)
		c.close()
		return false
	})

}

func (l *serverTcpListener) close() {
	select {
	case <-l.done:
		return
	default:
	}
	l.nconn.Close()
	l.clients.Each(func(i interface{}) bool {
		c := i.(*serverClient)
		c.close()
		return false
	})
	close(l.done)
}

func (l *serverTcpListener) forwardTrack(path string, id int, flow trackFlow, frame []byte) {
	// l.mutex.RLock()
	// defer l.mutex.RUnlock()
	l.clients.Each(func(i interface{}) bool {
		c := i.(*serverClient)
		state, streamProtocol, streamTracks, cPath := c.GetClientInfo()
		if cPath == path && state == _CLIENT_STATE_PLAY {
			if len(streamTracks)-1 >= id {
				t := streamTracks[id]
				if t == nil {
					return false
				}
			} else {
				return false
			}
			if streamProtocol == _STREAM_PROTOCOL_UDP {
				if flow == _TRACK_FLOW_RTP {
					select {
					case l.p.udplRtp.write <- &udpWrite{
						addr: &net.UDPAddr{
							IP:   c.ip(),
							Zone: c.zone(),
							Port: streamTracks[id].rtpPort,
						},
						buf: frame,
					}:
					default:
						skippedFrames.Inc()
					}
				} else {
					select {
					case l.p.udplRtcp.write <- &udpWrite{
						addr: &net.UDPAddr{
							IP:   c.ip(),
							Zone: c.zone(),
							Port: streamTracks[id].rtcpPort,
						},
						buf: frame,
					}:
					default:
						skippedFrames.Inc()
					}
				}

			} else {
				select {
				case c.write <- &gortsplib.InterleavedFrame{
					Channel: trackToInterleavedChannel(id, flow),
					Content: frame,
				}:
				default:
					skippedFrames.Inc()
				}
			}
		}
		return false
	})

}
