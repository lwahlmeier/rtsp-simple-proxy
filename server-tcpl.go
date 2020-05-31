package main

import (
	"net"
	"sync"

	"github.com/PremiereGlobal/stim/pkg/stimlog"
	"github.com/lwahlmeier/gortsplib"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var tcpConnections = promauto.NewCounter(prometheus.CounterOpts{
	Name: "total_tcp_connections",
	Help: "The total number tcp connections",
})

type serverTcpListener struct {
	p       *program
	nconn   *net.TCPListener
	mutex   sync.RWMutex
	clients map[*serverClient]struct{}
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
		p:       p,
		nconn:   nconn,
		clients: make(map[*serverClient]struct{}),
		done:    make(chan struct{}),
		log:     stimlog.GetLoggerWithPrefix("[TCP listener]"),
	}

	l.log.Info("opened on :{}", p.conf.Server.RtspPort)
	return l, nil
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

	// close clients
	var doneChans []chan struct{}
	func() {
		l.mutex.RLock()
		defer l.mutex.RUnlock()
		for c := range l.clients {
			c.close()
			doneChans = append(doneChans, c.done)
		}
	}()
	for _, c := range doneChans {
		<-c
	}

	close(l.done)
}

func (l *serverTcpListener) close() {
	l.nconn.Close()
	<-l.done
}

func (l *serverTcpListener) forwardTrack(path string, id int, flow trackFlow, frame []byte) {
	l.mutex.RLock()
	defer l.mutex.RUnlock()

	for c := range l.clients {
		if c.path == path && c.state == _CLIENT_STATE_PLAY {

			//Here we skip the track if its not in the list of tracks
			if len(c.streamTracks)-1 >= id {
				t := c.streamTracks[id]
				if t == nil {
					break
				}
			} else {
				break
			}

			if c.streamProtocol == _STREAM_PROTOCOL_UDP {
				if flow == _TRACK_FLOW_RTP {
					select {
					case l.p.udplRtp.write <- &udpWrite{
						addr: &net.UDPAddr{
							IP:   c.ip(),
							Zone: c.zone(),
							Port: c.streamTracks[id].rtpPort,
						},
						buf: frame,
					}:
					default:
					}
				} else {
					select {
					case l.p.udplRtcp.write <- &udpWrite{
						addr: &net.UDPAddr{
							IP:   c.ip(),
							Zone: c.zone(),
							Port: c.streamTracks[id].rtcpPort,
						},
						buf: frame,
					}:
					default:
					}
				}

			} else {
				select {
				case c.write <- &gortsplib.InterleavedFrame{
					Channel: trackToInterleavedChannel(id, flow),
					Content: frame,
				}:
				default:
				}
			}
		}
	}
}
