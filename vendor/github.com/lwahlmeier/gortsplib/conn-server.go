package gortsplib

import (
	"bytes"
	"fmt"
	"net"
	"time"
)

// ConnServerConf allows to configure a ConnServer.
type ConnServerConf struct {
	// pre-existing TCP connection that will be wrapped
	NConn net.Conn

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

// ConnServer is a server-side RTSP connection.
type ConnServer struct {
	conf ConnServerConf
	// br   *bufio.Reader
	// bw   *bufio.Writer
	tmpBuffer         []byte
	pendingReadBuffer bytes.Buffer
	conn              net.Conn
}

// NewConnServer allocates a ConnClient.
func NewConnServer(conf ConnServerConf) *ConnServer {
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
	conf.NConn.SetReadDeadline(time.Time{})
	return &ConnServer{
		conf: conf,
		// br:   bufio.NewReaderSize(conf.NConn, conf.ReadBufferSize),
		// bw:   bufio.NewWriterSize(conf.NConn, conf.ReadBufferSize),
		tmpBuffer: make([]byte, 4096),
		conn:      conf.NConn,
	}
}

// NetConn returns the underlying net.Conn.
func (s *ConnServer) NetConn() net.Conn {
	return s.conf.NConn
}

// ReadRequest reads a Request.
func (s *ConnServer) ReadRequest() (*Request, error) {
	for {
		n, err := s.conn.Read(s.tmpBuffer)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			s.pendingReadBuffer.Write(s.tmpBuffer[:n])
			for s.pendingReadBuffer.Len() > 0 {
				tmpBuff := s.pendingReadBuffer.Bytes()
				pos := bytes.Index(tmpBuff, []byte(HEADER_END))
				if pos > -1 {
					cl, err := getContentLength(string(tmpBuff[:pos]))
					if err != nil {
						return nil, err
					}
					if cl+pos+4 > len(tmpBuff) {
						break
					}
					r, err := readRequestFromBytes(tmpBuff[:pos+4+cl])
					if err != nil {
						return nil, err
					}
					s.pendingReadBuffer.Next(cl + pos + 4)
					return r, nil
				} else if pos == -1 && s.pendingReadBuffer.Len() > 1024*16 {
					return nil, fmt.Errorf("No RTSP header end after 16k")
				} else {
					break
				}
			}
		}
	}

}

// WriteResponse writes a response.
func (s *ConnServer) WriteResponse(res *Response) error {
	s.conn.SetWriteDeadline(time.Now().Add(s.conf.WriteTimeout))
	_, err := s.conn.Write([]byte(res.String()))
	return err
}

// ReadInterleavedFrame reads an InterleavedFrame.
func (s *ConnServer) ReadInterleavedFrame() (*InterleavedFrame, error) {
	s.conf.NConn.SetReadDeadline(time.Now().Add(s.conf.ReadTimeout))
	return readInterleavedFrame(s.conn)
}

// WriteInterleavedFrame writes an InterleavedFrame.
func (s *ConnServer) WriteInterleavedFrame(frame *InterleavedFrame) error {
	s.conn.SetWriteDeadline(time.Now().Add(s.conf.WriteTimeout))
	_, err := s.conn.Write(frame.ToBytes())
	return err
}
