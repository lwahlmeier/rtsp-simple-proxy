package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/PremiereGlobal/stim/pkg/stimlog"
	"github.com/lwahlmeier/gortsplib"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var rtspConnections = promauto.NewCounter(prometheus.CounterOpts{
	Name: "total_rtsp_connections",
	Help: "The total number rtsp connections",
})

func interleavedChannelToTrack(channel uint8) (int, trackFlow) {
	if (channel % 2) == 0 {
		return int(channel / 2), _TRACK_FLOW_RTP
	}
	return int((channel - 1) / 2), _TRACK_FLOW_RTCP
}

func trackToInterleavedChannel(id int, flow trackFlow) uint8 {
	if flow == _TRACK_FLOW_RTP {
		return uint8(id * 2)
	}
	return uint8((id * 2) + 1)
}

type clientState int

const (
	_CLIENT_STATE_STARTING clientState = iota
	_CLIENT_STATE_PRE_PLAY
	_CLIENT_STATE_PLAY
)

func (cs clientState) String() string {
	switch cs {
	case _CLIENT_STATE_STARTING:
		return "STARTING"

	case _CLIENT_STATE_PRE_PLAY:
		return "PRE_PLAY"

	case _CLIENT_STATE_PLAY:
		return "PLAY"
	}
	return "UNKNOWN"
}

type serverClient struct {
	p              *program
	conn           *gortsplib.ConnServer
	state          clientState
	path           string
	readAuth       *gortsplib.AuthServer
	stream         *stream
	streamProtocol streamProtocol
	streamTracks   []*track
	write          chan *gortsplib.InterleavedFrame
	done           chan struct{}
	log            stimlog.StimLogger
}

func newServerClient(p *program, nconn net.Conn) *serverClient {
	c := &serverClient{
		p: p,
		conn: gortsplib.NewConnServer(gortsplib.ConnServerConf{
			NConn:        nconn,
			ReadTimeout:  p.readTimeout,
			WriteTimeout: p.writeTimeout,
		}),
		state: _CLIENT_STATE_STARTING,
		write: make(chan *gortsplib.InterleavedFrame, 10),
		done:  make(chan struct{}),
		log:   stimlog.GetLoggerWithPrefix("[RTSP client " + nconn.RemoteAddr().String() + "]"),
	}

	c.p.tcpl.mutex.RLock()
	c.p.tcpl.clients[c] = struct{}{}
	c.p.tcpl.mutex.RUnlock()

	go c.run()

	return c
}

func (c *serverClient) SetState(state clientState) {
	c.p.tcpl.mutex.RLock()
	defer c.p.tcpl.mutex.RUnlock()
	c.state = state
}

func (c *serverClient) GetState() clientState {
	c.p.tcpl.mutex.RLock()
	defer c.p.tcpl.mutex.RUnlock()
	return c.state
}

func (c *serverClient) close() error {
	c.p.tcpl.mutex.RLock()
	defer c.p.tcpl.mutex.RUnlock()
	if _, ok := c.p.tcpl.clients[c]; !ok {
		return nil
	}

	delete(c.p.tcpl.clients, c)
	c.conn.NetConn().Close()
	close(c.write)

	return nil
}

func (c *serverClient) ip() net.IP {
	return c.conn.NetConn().RemoteAddr().(*net.TCPAddr).IP
}

func (c *serverClient) zone() string {
	return c.conn.NetConn().RemoteAddr().(*net.TCPAddr).Zone
}

func (c *serverClient) run() {
	c.log.Info("connected")

	for {
		req, err := c.conn.ReadRequest()
		if err != nil {
			if err != io.EOF {
				c.log.Warn(err)
			}
			break
		}

		ok := c.handleRequest(req)
		if !ok {
			break
		}
	}

	c.close()

	c.log.Info("disconnected")

	close(c.done)
}

func (c *serverClient) writeResError(req *gortsplib.Request, code gortsplib.StatusCode, err error) {
	c.log.Warn(err)

	header := gortsplib.Header{}
	if cseq, ok := req.Header["CSeq"]; ok && len(cseq) == 1 {
		header["CSeq"] = []string{cseq[0]}
	}

	c.conn.WriteResponse(&gortsplib.Response{
		StatusCode: code,
		Header:     header,
	})
}

var errAuthCritical = errors.New("auth critical")
var errAuthNotCritical = errors.New("auth not critical")

func (c *serverClient) validateAuth(req *gortsplib.Request, user string, pass string, auth **gortsplib.AuthServer) error {
	if user == "" {
		return nil
	}

	initialRequest := false
	if *auth == nil {
		initialRequest = true
		*auth = gortsplib.NewAuthServer(user, pass)
	}

	err := (*auth).ValidateHeader(req.Header["Authorization"], req.Method, req.Url)
	if err != nil {
		if !initialRequest {
			c.log.Warn("Unauthorized: {}", err)
		}

		c.conn.WriteResponse(&gortsplib.Response{
			StatusCode: gortsplib.StatusUnauthorized,
			Header: gortsplib.Header{
				"CSeq":             []string{req.Header["CSeq"][0]},
				"WWW-Authenticate": (*auth).GenerateHeader(),
			},
		})

		if !initialRequest {
			return errAuthCritical
		}

		return errAuthNotCritical
	}

	return nil
}

func (c *serverClient) handleRequest(req *gortsplib.Request) bool {
	c.log.Info(string(req.Method))

	var ok bool = false

	cseq, ok := req.Header["CSeq"]
	if !ok || len(cseq) != 1 {
		c.writeResError(req, gortsplib.StatusBadRequest, fmt.Errorf("cseq missing"))
		return false
	}

	if c.path == "" {
		tp := strings.Split(strings.Trim(req.Url.Path, "/"), "/")
		c.stream, ok = c.p.streams[tp[0]]
		if !ok {
			c.writeResError(req, gortsplib.StatusBadRequest, fmt.Errorf("there is no stream on path '%s'", tp[0]))
			return false
		}
		c.path = tp[0]
	}

	if c.stream.GetState() != _STREAM_STATE_READY {
		c.writeResError(req, gortsplib.StatusBadRequest, fmt.Errorf("stream '%s' is not ready yet", c.path))
		return false
	}
	sdp := c.stream.serverSdpText

	switch req.Method {
	case gortsplib.OPTIONS:
		// do not check state, since OPTIONS can be requested
		// in any state

		c.conn.WriteResponse(&gortsplib.Response{
			StatusCode: gortsplib.StatusOK,
			Header: gortsplib.Header{
				"CSeq": []string{cseq[0]},
				"Public": []string{strings.Join([]string{
					string(gortsplib.DESCRIBE),
					string(gortsplib.SETUP),
					string(gortsplib.PLAY),
					string(gortsplib.PAUSE),
					string(gortsplib.TEARDOWN),
				}, ", ")},
			},
		})
		return true

	case gortsplib.DESCRIBE:
		if c.GetState() != _CLIENT_STATE_STARTING {
			c.writeResError(req, gortsplib.StatusBadRequest,
				fmt.Errorf("client is in state '%s' instead of '%s'", c.GetState(), _CLIENT_STATE_STARTING))
			return false
		}

		err := c.validateAuth(req, c.p.conf.Server.ReadUser, c.p.conf.Server.ReadPass, &c.readAuth)
		if err != nil {
			if err == errAuthCritical {
				return false
			}
			return true
		}

		c.conn.WriteResponse(&gortsplib.Response{
			StatusCode: gortsplib.StatusOK,
			Header: gortsplib.Header{
				"CSeq":         []string{cseq[0]},
				"Content-Base": []string{req.Url.String()},
				"Content-Type": []string{"application/sdp"},
			},
			Content: sdp,
		})
		return true

	case gortsplib.SETUP:
		tsRaw, ok := req.Header["Transport"]
		if !ok || len(tsRaw) != 1 {
			c.writeResError(req, gortsplib.StatusBadRequest, fmt.Errorf("transport header missing"))
			return false
		}

		th := gortsplib.ReadHeaderTransport(tsRaw[0])

		if _, ok := th["unicast"]; !ok {
			c.writeResError(req, gortsplib.StatusBadRequest, fmt.Errorf("transport header does not contain unicast"))
			return false
		}

		switch c.GetState() {
		// play
		case _CLIENT_STATE_STARTING, _CLIENT_STATE_PRE_PLAY:
			err := c.validateAuth(req, c.p.conf.Server.ReadUser, c.p.conf.Server.ReadPass, &c.readAuth)
			if err != nil {
				if err == errAuthCritical {
					return false
				}
				return true
			}

			_, ok1 := th["RTP/AVP"]
			_, ok2 := th["RTP/AVP/UDP"]
			if ok1 || ok2 {
				if _, ok := c.p.protocols[_STREAM_PROTOCOL_UDP]; !ok {
					c.writeResError(req, gortsplib.StatusUnsupportedTransport, fmt.Errorf("UDP streaming is disabled"))
					return false
				}

				rtpPort, rtcpPort := th.GetPorts("client_port")
				if rtpPort == 0 || rtcpPort == 0 {
					c.writeResError(req, gortsplib.StatusBadRequest, fmt.Errorf("transport header does not have valid client ports (%s)", tsRaw[0]))
					return false
				}

				if len(c.streamTracks) > 0 && c.streamProtocol != _STREAM_PROTOCOL_UDP {
					c.writeResError(req, gortsplib.StatusBadRequest, fmt.Errorf("client want to send tracks with different protocols"))
					return false
				}

				if len(c.streamTracks) >= len(c.stream.serverSdpParsed.Medias) {
					c.writeResError(req, gortsplib.StatusBadRequest, fmt.Errorf("all the tracks have already been setup"))
					return false
				}
				c.streamProtocol = _STREAM_PROTOCOL_UDP
				c.streamTracks = append(c.streamTracks, &track{
					rtpPort:  rtpPort,
					rtcpPort: rtcpPort,
				})
				c.SetState(_CLIENT_STATE_PRE_PLAY)

				c.conn.WriteResponse(&gortsplib.Response{
					StatusCode: gortsplib.StatusOK,
					Header: gortsplib.Header{
						"CSeq": []string{cseq[0]},
						"Transport": []string{strings.Join([]string{
							"RTP/AVP/UDP",
							"unicast",
							fmt.Sprintf("client_port=%d-%d", rtpPort, rtcpPort),
							fmt.Sprintf("server_port=%d-%d", c.p.conf.Server.RtpPort, c.p.conf.Server.RtcpPort),
						}, ";")},
						"Session": []string{"12345678"},
					},
				})
				return true

				// play via TCP
			} else if _, ok := th["RTP/AVP/TCP"]; ok {
				if _, ok := c.p.protocols[_STREAM_PROTOCOL_TCP]; !ok {
					c.writeResError(req, gortsplib.StatusUnsupportedTransport, fmt.Errorf("TCP streaming is disabled"))
					return false
				}
				if len(c.streamTracks) > 0 && c.streamProtocol != _STREAM_PROTOCOL_TCP {
					c.writeResError(req, gortsplib.StatusBadRequest, fmt.Errorf("client want to send tracks with different protocols"))
					return false
				}
				if len(c.streamTracks) >= len(c.stream.serverSdpParsed.Medias) {
					c.writeResError(req, gortsplib.StatusBadRequest, fmt.Errorf("all the tracks have already been setup"))
					return false
				}

				c.streamProtocol = _STREAM_PROTOCOL_TCP
				c.streamTracks = append(c.streamTracks, &track{
					rtpPort:  0,
					rtcpPort: 0,
				})

				c.SetState(_CLIENT_STATE_PRE_PLAY)

				interleaved := fmt.Sprintf("%d-%d", ((len(c.streamTracks) - 1) * 2), ((len(c.streamTracks)-1)*2)+1)

				c.conn.WriteResponse(&gortsplib.Response{
					StatusCode: gortsplib.StatusOK,
					Header: gortsplib.Header{
						"CSeq": []string{cseq[0]},
						"Transport": []string{strings.Join([]string{
							"RTP/AVP/TCP",
							"unicast",
							fmt.Sprintf("interleaved=%s", interleaved),
						}, ";")},
						"Session": []string{"12345678"},
					},
				})
				return true

			} else {
				c.writeResError(req, gortsplib.StatusBadRequest, fmt.Errorf("transport header does not contain a valid protocol (RTP/AVP, RTP/AVP/UDP or RTP/AVP/TCP) (%s)", tsRaw[0]))
				return false
			}

		default:
			c.writeResError(req, gortsplib.StatusBadRequest, fmt.Errorf("client is in state '%s'", c.state))
			return false
		}

	case gortsplib.PLAY:
		if c.GetState() != _CLIENT_STATE_PRE_PLAY {
			c.writeResError(req, gortsplib.StatusBadRequest, fmt.Errorf("client is in state '%s' instead of '%s'", c.state, _CLIENT_STATE_PRE_PLAY))
			return false
		}

		// Allow not all tracks to be sent
		// if len(c.streamTracks) != len(str.serverSdpParsed.Medias) {
		// 	return fmt.Errorf("not all tracks have been setup")
		// }
		//TODO: Need to fix so tracks can be sent in any order, if they are done out of order wrong tracks will be sent

		// first write response, then set state
		// otherwise, in case of TCP connections, RTP packets could be written
		// before the response
		c.conn.WriteResponse(&gortsplib.Response{
			StatusCode: gortsplib.StatusOK,
			Header: gortsplib.Header{
				"CSeq":    []string{cseq[0]},
				"Session": []string{"12345678"},
			},
		})

		c.log.Info("is receiving on path '{}', {} tracks via {}", c.path, len(c.streamTracks), c.streamProtocol)

		c.SetState(_CLIENT_STATE_PLAY)

		// when protocol is TCP, the RTSP connection becomes a RTP connection
		if c.streamProtocol == _STREAM_PROTOCOL_TCP {
			// write RTP frames sequentially
			go func() {
				for {
					frame, alive := <-c.write
					if alive {
						err := c.conn.WriteInterleavedFrame(frame)
						if err != nil {
							c.log.Warn("Client write error: {}", err)
							c.close()
							break
						}
					} else {
						c.log.Info("Exiting Write Loop")
						c.close()
						break
					}
				}
			}()

			// TODO: we should still be parsing RTSP here allowing it to go back to main reader
			buf := make([]byte, 2048)
			for {
				_, err := c.conn.NetConn().Read(buf)
				if err != nil {
					if err != io.EOF {
						c.log.Warn(err)
					}
					return false
				}
			}
		}

		return true

	case gortsplib.PAUSE:
		if c.state != _CLIENT_STATE_PLAY {
			c.writeResError(req, gortsplib.StatusBadRequest, fmt.Errorf("client is in state '%s' instead of '%s'", c.state, _CLIENT_STATE_PLAY))
			return false
		}

		c.log.Info("paused")

		c.SetState(_CLIENT_STATE_PRE_PLAY)

		c.conn.WriteResponse(&gortsplib.Response{
			StatusCode: gortsplib.StatusOK,
			Header: gortsplib.Header{
				"CSeq":    []string{cseq[0]},
				"Session": []string{"12345678"},
			},
		})
		return true

	case gortsplib.TEARDOWN:
		// close connection silently
		return false

	default:
		c.writeResError(req, gortsplib.StatusBadRequest, fmt.Errorf("unhandled method '%s'", req.Method))
		return false
	}
}
