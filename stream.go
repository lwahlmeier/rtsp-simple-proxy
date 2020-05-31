package main

import (
	"fmt"
	"math/rand"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PremiereGlobal/stim/pkg/stimlog"
	"github.com/lwahlmeier/gortsplib"
	"gortc.io/sdp"
)

const (
	_DIAL_TIMEOUT          = 10 * time.Second
	_RETRY_INTERVAL        = 5 * time.Second
	_CHECK_STREAM_INTERVAL = 6 * time.Second
	_STREAM_DEAD_AFTER     = 5 * time.Second
	_KEEPALIVE_INTERVAL    = 60 * time.Second
)

type streamUdpListenerPair struct {
	udplRtp  *streamUdpListener
	udplRtcp *streamUdpListener
}

type streamState int

const (
	_STREAM_STATE_STARTING streamState = iota
	_STREAM_STATE_READY
)

type stream struct {
	p               *program
	state           streamState
	path            string
	conf            streamConf
	ur              *url.URL
	proto           streamProtocol
	clientSdpParsed *sdp.Message
	serverSdpText   []byte
	serverSdpParsed *sdp.Message
	firstTime       bool
	terminate       chan struct{}
	done            chan struct{}
	log             stimlog.StimLogger
	mutex           sync.RWMutex
}

func newStream(p *program, path string, conf streamConf) (*stream, error) {
	ur, err := url.Parse(conf.Url)
	if err != nil {
		return nil, err
	}

	if ur.Port() == "" {
		ur.Host = ur.Hostname() + ":554"
	}
	if conf.Protocol == "" {
		conf.Protocol = "udp"
	}

	if ur.Scheme != "rtsp" {
		return nil, fmt.Errorf("unsupported scheme: %s", ur.Scheme)
	}
	if ur.User != nil {
		pass, _ := ur.User.Password()
		user := ur.User.Username()
		if user != "" && pass == "" ||
			user == "" && pass != "" {
			return nil, fmt.Errorf("username and password must be both provided")
		}
	}

	var proto = _STREAM_PROTOCOL_UDP
	if conf.Protocol == "udp" {
		proto = _STREAM_PROTOCOL_UDP
	} else if conf.Protocol == "tcp" {
		proto = _STREAM_PROTOCOL_TCP
	} else {
		return nil, fmt.Errorf("unsupported protocol: '%v'", conf.Protocol)
	}

	s := &stream{
		p:         p,
		state:     _STREAM_STATE_STARTING,
		path:      path,
		conf:      conf,
		ur:        ur,
		proto:     proto,
		firstTime: true,
		terminate: make(chan struct{}),
		done:      make(chan struct{}),
		log:       stimlog.GetLoggerWithPrefix("[STREAM " + path + "]"),
	}
	return s, nil
}

func (s *stream) GetState() streamState {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.state
}

func (s *stream) GetPath() string {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.path
}

func (s *stream) SetState(ss streamState) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.state = ss
}

func (s *stream) SetSDP(clientSdpParsed *sdp.Message, serverSdpParsed *sdp.Message, serverSdpText []byte) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.clientSdpParsed = clientSdpParsed
	s.serverSdpText = serverSdpText
	s.serverSdpParsed = serverSdpParsed
}

func (s *stream) run() {
	for {
		ok := s.do()
		if !ok {
			break
		}
	}

	s.close()
}

func (s *stream) do() bool {
	if s.firstTime {
		s.firstTime = false
	} else {
		t := time.NewTimer(_RETRY_INTERVAL)
		select {
		case <-s.terminate:
			return false
		case <-t.C:
		}
	}

	s.log.Info("initializing with protocol {}", s.proto)

	var nconn net.Conn
	var err error
	dialDone := make(chan struct{})
	go func() {
		nconn, err = net.DialTimeout("tcp", s.ur.Host, _DIAL_TIMEOUT)
		close(dialDone)
	}()

	select {
	case <-s.terminate:
		return false
	case <-dialDone:
	}

	if err != nil {
		s.log.Warn(err)
		return true
	}
	defer nconn.Close()
	user := ""
	pass := ""
	if s.ur.User != nil {
		user = s.ur.User.Username()
		pass, _ = s.ur.User.Password()
	}

	conn, err := gortsplib.NewConnClient(gortsplib.ConnClientConf{
		NConn:        nconn,
		Username:     user,
		Password:     pass,
		ReadTimeout:  s.p.readTimeout,
		WriteTimeout: s.p.writeTimeout,
	})
	if err != nil {
		s.log.Warn(err)
		return true
	}

	res, err := conn.WriteRequest(&gortsplib.Request{
		Method: gortsplib.OPTIONS,
		Url: &url.URL{
			Scheme: "rtsp",
			Host:   s.ur.Host,
			Path:   "/",
		},
	})
	if err != nil {
		s.log.Warn(err)
		return true
	}

	if res.StatusCode != gortsplib.StatusOK {
		s.log.Warn("OPTIONS returned code {} ({})", res.StatusCode, res.StatusMessage)
		return true
	}

	res, err = conn.WriteRequest(&gortsplib.Request{
		Method: gortsplib.DESCRIBE,
		Url: &url.URL{
			Scheme:   "rtsp",
			Host:     s.ur.Host,
			Path:     s.ur.Path,
			RawQuery: s.ur.RawQuery,
		},
	})
	if err != nil {
		s.log.Warn(err)
		return true
	}

	if res.StatusCode != gortsplib.StatusOK {
		s.log.Warn("DESCRIBE returned code {} ({})", res.StatusCode, res.StatusMessage)
		return true
	}

	contentType, ok := res.Header["Content-Type"]
	if !ok || len(contentType) != 1 {
		s.log.Warn("Content-Type not provided")
		return true
	}

	if contentType[0] != "application/sdp" {
		s.log.Warn("wrong Content-Type, expected application/sdp")
		return true
	}

	clientSdpParsed, err := gortsplib.SDPParse(res.Content)
	if err != nil {
		s.log.Warn("invalid SDP: {}", err)
		return true
	}

	// create a filtered SDP that is used by the server (not by the client)
	serverSdpParsed, serverSdpText := gortsplib.SDPFilter(clientSdpParsed, res.Content)
	s.SetSDP(clientSdpParsed, serverSdpParsed, serverSdpText)

	if s.proto == _STREAM_PROTOCOL_UDP {
		return s.runUdp(conn)
	} else {
		return s.runTcp(conn)
	}
}

func (s *stream) runUdp(conn *gortsplib.ConnClient) bool {
	publisherIp := conn.NetConn().RemoteAddr().(*net.TCPAddr).IP

	var streamUdpListenerPairs []streamUdpListenerPair

	defer func() {
		for _, pair := range streamUdpListenerPairs {
			pair.udplRtp.close()
			pair.udplRtcp.close()
		}
	}()

	for i, media := range s.clientSdpParsed.Medias {
		var rtpPort int
		var rtcpPort int
		var udplRtp *streamUdpListener
		var udplRtcp *streamUdpListener
		for {
			// choose two consecutive ports in range 65536-10000
			// rtp must be pair and rtcp odd
			rtpPort = (rand.Intn((65535-10000)/2) * 2) + 10000
			rtcpPort = rtpPort + 1

			var err error
			udplRtp, err = newStreamUdpListener(s.p, rtpPort)
			if err != nil {
				continue
			}

			udplRtcp, err = newStreamUdpListener(s.p, rtcpPort)
			if err != nil {
				udplRtp.close()
				continue
			}
			break
		}

		ret := s.ur.Path

		if len(ret) == 0 || ret[len(ret)-1] != '/' {
			ret += "/"
		}

		control := media.Attributes.Value("control")
		if control != "" {
			ret += control
		} else {
			ret += "trackID=" + strconv.FormatInt(int64(i+1), 10)
		}

		res, err := conn.WriteRequest(&gortsplib.Request{
			Method: gortsplib.SETUP,
			Url: &url.URL{
				Scheme:   "rtsp",
				Host:     s.ur.Host,
				Path:     ret,
				RawQuery: s.ur.RawQuery,
			},
			Header: gortsplib.Header{
				"Transport": []string{strings.Join([]string{
					"RTP/AVP/UDP",
					"unicast",
					fmt.Sprintf("client_port=%d-%d", rtpPort, rtcpPort),
				}, ";")},
			},
		})
		if err != nil {
			s.log.Warn(err)
			udplRtp.close()
			udplRtcp.close()
			return true
		}

		if res.StatusCode != gortsplib.StatusOK {
			s.log.Warn("SETUP returned code {} ({})", res.StatusCode, res.StatusMessage)
			udplRtp.close()
			udplRtcp.close()
			return true
		}

		tsRaw, ok := res.Header["Transport"]
		if !ok || len(tsRaw) != 1 {
			s.log.Warn("transport header not provided")
			udplRtp.close()
			udplRtcp.close()
			return true
		}

		th := gortsplib.ReadHeaderTransport(tsRaw[0])
		rtpServerPort, rtcpServerPort := th.GetPorts("server_port")
		if rtpServerPort == 0 {
			s.log.Warn("server ports not provided")
			udplRtp.close()
			udplRtcp.close()
			return true
		}

		udplRtp.publisherIp = publisherIp
		udplRtp.publisherPort = rtpServerPort
		udplRtp.trackId = i
		udplRtp.flow = _TRACK_FLOW_RTP
		udplRtp.path = s.path

		udplRtcp.publisherIp = publisherIp
		udplRtcp.publisherPort = rtcpServerPort
		udplRtcp.trackId = i
		udplRtcp.flow = _TRACK_FLOW_RTCP
		udplRtcp.path = s.path

		streamUdpListenerPairs = append(streamUdpListenerPairs, streamUdpListenerPair{
			udplRtp:  udplRtp,
			udplRtcp: udplRtcp,
		})
	}

	res, err := conn.WriteRequest(&gortsplib.Request{
		Method: gortsplib.PLAY,
		Url: &url.URL{
			Scheme:   "rtsp",
			Host:     s.ur.Host,
			Path:     s.ur.Path,
			RawQuery: s.ur.RawQuery,
		},
	})
	if err != nil {
		s.log.Warn(err)
		return true
	}

	if res.StatusCode != gortsplib.StatusOK {
		s.log.Warn("PLAY returned code {} ({})", res.StatusCode, res.StatusMessage)
		return true
	}

	for _, pair := range streamUdpListenerPairs {
		pair.udplRtp.start()
		pair.udplRtcp.start()
	}

	tickerSendKeepalive := time.NewTicker(_KEEPALIVE_INTERVAL)
	defer tickerSendKeepalive.Stop()

	tickerCheckStream := time.NewTicker(_CHECK_STREAM_INTERVAL)
	defer tickerSendKeepalive.Stop()
	s.SetState(_STREAM_STATE_READY)

	defer func() {
		s.SetState(_STREAM_STATE_STARTING)
		// disconnect all clients
		s.p.tcpl.closeClientsOnPath(s.GetPath())
	}()

	s.log.Info("ready")

	for {
		select {
		case <-s.terminate:
			return false

		case <-tickerSendKeepalive.C:
			_, err = conn.WriteRequest(&gortsplib.Request{
				Method: gortsplib.OPTIONS,
				Url: &url.URL{
					Scheme: "rtsp",
					Host:   s.ur.Host,
					Path:   "/",
				},
			})
			if err != nil {
				s.log.Warn(err)
				return true
			}

		case <-tickerCheckStream.C:
			lastFrameTime := time.Time{}

			getLastFrameTime := func(l *streamUdpListener) {
				l.mutex.Lock()
				defer l.mutex.Unlock()
				if l.lastFrameTime.After(lastFrameTime) {
					lastFrameTime = l.lastFrameTime
				}
			}

			for _, pair := range streamUdpListenerPairs {
				getLastFrameTime(pair.udplRtp)
				getLastFrameTime(pair.udplRtcp)
			}

			if time.Since(lastFrameTime) >= _STREAM_DEAD_AFTER {
				s.log.Warn("stream is dead")
				return true
			}
		}
	}
}

func (s *stream) runTcp(conn *gortsplib.ConnClient) bool {
	for i, media := range s.clientSdpParsed.Medias {
		interleaved := fmt.Sprintf("interleaved=%d-%d", (i * 2), (i*2)+1)
		ret := s.ur.Path

		if len(ret) == 0 || ret[len(ret)-1] != '/' {
			ret += "/"
		}

		control := media.Attributes.Value("control")
		if control != "" {
			ret += control
		} else {
			ret += "trackID=" + strconv.FormatInt(int64(i+1), 10)
		}
		res, err := conn.WriteRequest(&gortsplib.Request{
			Method: gortsplib.SETUP,
			Url: &url.URL{
				Scheme:   "rtsp",
				Host:     s.ur.Host,
				Path:     ret,
				RawQuery: s.ur.RawQuery,
			},
			Header: gortsplib.Header{
				"Transport": []string{strings.Join([]string{
					"RTP/AVP/TCP",
					"unicast",
					interleaved,
				}, ";")},
			},
		})
		if err != nil {
			s.log.Warn(err)
			return true
		}

		if res.StatusCode != gortsplib.StatusOK {
			s.log.Warn("SETUP returned code {} ({})", res.StatusCode, res.StatusMessage)
			return true
		}

		tsRaw, ok := res.Header["Transport"]
		if !ok || len(tsRaw) != 1 {
			s.log.Warn("transport header not provided")
			return true
		}

		th := gortsplib.ReadHeaderTransport(tsRaw[0])

		_, ok = th[interleaved]
		if !ok {
			s.log.Warn("transport header does not have {} ({})", interleaved, tsRaw[0])
			return true
		}
	}

	res, err := conn.WriteRequest(&gortsplib.Request{
		Method: gortsplib.PLAY,
		Url: &url.URL{
			Scheme:   "rtsp",
			Host:     s.ur.Host,
			Path:     s.ur.Path,
			RawQuery: s.ur.RawQuery,
		},
	})
	if err != nil {
		s.log.Warn(err)
		return true
	}

	if res.StatusCode != gortsplib.StatusOK {
		s.log.Warn("PLAY returned code {} ({})", res.StatusCode, res.StatusMessage)
		return true
	}
	s.SetState(_STREAM_STATE_READY)

	defer func() {
		s.SetState(_STREAM_STATE_STARTING)
		// disconnect all clients
		s.p.tcpl.closeClientsOnPath(s.GetPath())
	}()

	s.log.Info("ready")

	chanConnError := make(chan struct{})
	go func() {
		for {
			frame, err := conn.ReadInterleavedFrame()
			if err != nil {
				s.log.Warn("ReadInterleavedFrame ERR: {}", err)
				close(chanConnError)
				break
			}
			trackID, trackFlow := interleavedChannelToTrack(frame.Channel)
			s.p.tcpl.forwardTrack(s.path, trackID, trackFlow, frame.Content)
		}
	}()

	for {
		select {
		case <-time.After(15 * time.Second):
			s.log.Info("Sending Ping")
			_, err = conn.WriteRequest(&gortsplib.Request{
				Method: gortsplib.GET_PARAMETER,
				Url: &url.URL{
					Scheme: "rtsp",
					Host:   s.ur.Host,
					Path:   "/",
				},
			})
			if err != nil {
				s.log.Warn(err)
				return true
			}
		case <-s.terminate:
			return false
		case <-chanConnError:
			s.log.Warn("Got Close!")
			return true
		}
	}
}

func (s *stream) close() {
	select {
	case <-s.done:
		return
	default:
		close(s.done)
	}
	close(s.terminate)

}
