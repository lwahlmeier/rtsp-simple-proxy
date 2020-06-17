package gortsplib

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"time"
)

const HEADER_END = "\r\n\r\n"

// ConnClientConf allows to configure a ConnClient.
type ConnClientConf struct {
	// pre-existing TCP connection that will be wrapped
	NConn net.Conn

	// (optional) a username that will be sent to the server when requested
	Username string

	// (optional) a password that will be sent to the server when requested
	Password string

	// (optional) timeout for read requests.
	// It defaults to 5 seconds
	ReadTimeout time.Duration

	// (optional) timeout for write requests.
	// It defaults to 5 seconds
	WriteTimeout time.Duration

	// (optional) size of the read buffer.
	// It defaults to 4096 bytes
	ReadBufferSize int

	// (optional) size of the write buffer.
	// It defaults to 4096 bytes
	WriteBufferSize int
}

type RTSPResponse struct {
	Response *Response
	Error    error
}

type FrameResponse struct {
	Frame *InterleavedFrame
	Error error
}

// ConnClient is a client-side RTSP connection.
type ConnClient struct {
	conf      ConnClientConf
	br        *bufio.Reader
	bw        *bufio.Writer
	session   string
	curCSeq   int
	auth      *AuthClient
	rtspData  chan *RTSPResponse
	frameData chan *FrameResponse
	readError error
	conn      net.Conn
}

// NewConnClient allocates a ConnClient. See ConnClientConf for the options.
func NewConnClient(conf ConnClientConf) (*ConnClient, error) {
	if conf.ReadTimeout == time.Duration(0) {
		conf.ReadTimeout = 5 * time.Second
	}
	if conf.WriteTimeout == time.Duration(0) {
		conf.WriteTimeout = 5 * time.Second
	}
	if conf.ReadBufferSize == 0 {
		conf.ReadBufferSize = 4096
	}
	if conf.WriteBufferSize == 0 {
		conf.WriteBufferSize = 4096
	}

	if conf.Username != "" && conf.Password == "" ||
		conf.Username == "" && conf.Password != "" {
		return nil, fmt.Errorf("username and password must be both provided")
	}

	cc := &ConnClient{
		conf:      conf,
		br:        bufio.NewReaderSize(conf.NConn, conf.ReadBufferSize),
		bw:        bufio.NewWriterSize(conf.NConn, conf.WriteBufferSize),
		conn:      conf.NConn,
		rtspData:  make(chan *RTSPResponse),
		frameData: make(chan *FrameResponse, 10),
	}
	go cc.doRead()
	return cc, nil
}

func (c *ConnClient) doRead() {
	readBuff := make([]byte, 4096)
	pendingBuff := bytes.Buffer{}
	for {
		n, err := c.conn.Read(readBuff)
		if err != nil {
			fmt.Printf("Got Read Error %s\n", err)
			select {
			case c.frameData <- &FrameResponse{Frame: nil, Error: err}:
			case c.rtspData <- &RTSPResponse{Response: nil, Error: err}:
			}
			return
		}
		if n > 0 {
			pendingBuff.Write(readBuff[:n])
			for pendingBuff.Len() > 0 {
				tmpBuff := pendingBuff.Bytes()
				if tmpBuff[0] == 0x24 {
					if pendingBuff.Len() >= 4 {
						framelen := int(binary.BigEndian.Uint16(tmpBuff[2:]))
						if framelen > _INTERLEAVED_FRAME_MAX_SIZE {
							fmt.Printf("frame Error\n")
							c.frameData <- &FrameResponse{Frame: nil, Error: fmt.Errorf("Got interleaved frame that was to large")}
							return
						} else if framelen > len(tmpBuff)-4 {
							break
						} else {
							f := &InterleavedFrame{
								Channel: tmpBuff[1],
								Content: make([]byte, framelen),
							}
							pendingBuff.Next(4)
							pendingBuff.Read(f.Content)
							select {
							case c.frameData <- &FrameResponse{Frame: f, Error: nil}:
							case <-time.After(10 * time.Millisecond):
								fmt.Printf("Skipped frame\n")
							}
						}
					} else {
						break
					}
				} else {
					pos := bytes.Index(tmpBuff, []byte(HEADER_END))
					if pos > -1 {
						cl, err := getContentLength(string(tmpBuff[:pos]))
						if err != nil {
							fmt.Printf("Reading RTSP err1:%s\n", err)
							c.rtspData <- &RTSPResponse{Response: nil, Error: err}
							return
						}
						if cl+pos+4 > len(tmpBuff) {
							break
						}
						r, err := readResponseFromBytes(tmpBuff[:pos+4+cl])
						if err != nil {
							fmt.Printf("Reading RTSP err2:%s\n", err)
							c.rtspData <- &RTSPResponse{Response: nil, Error: err}
							return
						}
						pendingBuff.Next(cl + pos + 4)
						select {
						case c.rtspData <- &RTSPResponse{Response: r, Error: nil}:
						case <-time.After(10 * time.Millisecond):
						}
					} else {
						break
					}
				}
			}
		}
	}
}

// NetConn returns the underlying net.Conn.
func (c *ConnClient) NetConn() net.Conn {
	return c.conf.NConn
}

// WriteRequest writes a request and reads a response.
func (c *ConnClient) WriteRequest(req *Request) (*Response, error) {
	if req.Header == nil {
		req.Header = make(Header)
	}

	// insert session
	if c.session != "" {
		req.Header["Session"] = []string{c.session}
	}

	// insert auth
	if c.auth != nil {
		req.Header["Authorization"] = c.auth.GenerateHeader(req.Method, req.Url)
	}

	// insert cseq
	c.curCSeq++
	req.Header["CSeq"] = []string{strconv.FormatInt(int64(c.curCSeq), 10)}

	c.conf.NConn.SetWriteDeadline(time.Now().Add(c.conf.WriteTimeout))
	_, err := c.conn.Write([]byte(req.String()))
	if err != nil {
		return nil, err
	}

	select {
	case rr := <-c.rtspData:
		if rr.Error != nil {
			return nil, err
		}
		// get session from response
		if sxRaw, ok := rr.Response.Header["Session"]; ok && len(sxRaw) == 1 {
			sx, err := ReadHeaderSession(sxRaw[0])
			if err != nil {
				return nil, fmt.Errorf("unable to parse session header: %s", err)
			}
			c.session = sx.Session
		}

		// setup authentication
		if rr.Response.StatusCode == StatusUnauthorized && c.conf.Username != "" && c.auth == nil {
			auth, err := NewAuthClient(rr.Response.Header["WWW-Authenticate"], c.conf.Username, c.conf.Password)
			if err != nil {
				return nil, fmt.Errorf("unable to setup authentication: %s", err)
			}
			c.auth = auth

			// send request again
			return c.WriteRequest(req)
		}

		return rr.Response, nil
	case <-time.After(c.conf.ReadTimeout):
		return nil, fmt.Errorf("rtsp read timeout")
	}
}

// ReadInterleavedFrame reads an InterleavedFrame.
func (c *ConnClient) ReadInterleavedFrame() (*InterleavedFrame, error) {
	select {
	case fr := <-c.frameData:
		return fr.Frame, fr.Error
	case <-time.After(c.conf.ReadTimeout):
		return nil, fmt.Errorf("rtp read timeout")
	}
}

// WriteInterleavedFrame writes an InterleavedFrame.
func (c *ConnClient) WriteInterleavedFrame(frame *InterleavedFrame) error {
	c.conn.SetWriteDeadline(time.Now().Add(c.conf.WriteTimeout))
	_, err := c.conn.Write(frame.ToBytes())
	return err
}
