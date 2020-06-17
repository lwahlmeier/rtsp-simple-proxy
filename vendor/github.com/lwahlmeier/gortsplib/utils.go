package gortsplib

import (
	"bytes"
	"fmt"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

const (
	_RTSP_PROTO         = "RTSP/1.0"
	END_LINE            = "\r\n"
	_MAX_CONTENT_LENGTH = 4096
)

type SimpleBuffer struct {
	buffer []byte
	size   int
}

func NewSimpleBuffer(alloc int) *SimpleBuffer {
	return &SimpleBuffer{buffer: make([]byte, alloc), size: 0}
}

func (sb *SimpleBuffer) GetRawBuffer() []byte {
	return sb.buffer
}

func (sb *SimpleBuffer) GetSizedBuffer() []byte {
	return sb.buffer[:sb.size]
}

func (sb *SimpleBuffer) GetSize() int {
	return sb.size
}

func (sb *SimpleBuffer) SetSize(size int) {
	sb.size = size
}

type ReuseBuffer struct {
	buffers  []*SimpleBuffer
	minAlloc int
	maxSpare int
	lock     sync.Mutex
}

var reuseBuffer = &ReuseBuffer{minAlloc: 4096, maxSpare: 100}

func GetReuseBuffer() *ReuseBuffer {
	return reuseBuffer
}

func (rb *ReuseBuffer) SetDefaultAlloc(size int) {
	rb.lock.Lock()
	defer rb.lock.Unlock()
	rb.minAlloc = size
}

func (rb *ReuseBuffer) SetMaxSpare(size int) {
	rb.lock.Lock()
	defer rb.lock.Unlock()
	rb.maxSpare = size
}

func (rb *ReuseBuffer) GetBuffer() *SimpleBuffer {
	rb.lock.Lock()
	defer rb.lock.Unlock()
	if len(rb.buffers) == 0 {
		sb := NewSimpleBuffer(rb.minAlloc)
		runtime.SetFinalizer(sb, func(sb2 *SimpleBuffer) {
			GetReuseBuffer().ReturnBuffer(sb2)
		})
		return sb
	}
	sb := rb.buffers[0]
	rb.buffers = rb.buffers[1:]
	return sb
}

func (rb *ReuseBuffer) ReturnBuffer(sb *SimpleBuffer) {
	if len(sb.GetRawBuffer()) < rb.minAlloc {
		runtime.SetFinalizer(sb, nil)
		return
	}
	rb.lock.Lock()
	defer rb.lock.Unlock()
	if len(rb.buffers) >= rb.maxSpare {
		runtime.SetFinalizer(sb, nil)
		return
	}
	sb.SetSize(0)
	rb.buffers = append(rb.buffers, sb)
}

func readRequestFromBytes(hs []byte) (*Request, error) {
	req := &Request{}

	ehpos := bytes.Index(hs, []byte(END_LINE+END_LINE))
	if ehpos == -1 {
		return nil, fmt.Errorf("Could not find end of reqest to parse")
	}

	erhpos := bytes.Index(hs, []byte(END_LINE))
	rh := strings.SplitN(string(hs[:erhpos]), " ", 3)
	if len(rh) < 3 {
		return nil, fmt.Errorf("unable to parse Request Header")
	}
	req.Method = Method(rh[0])

	rawURL := string(rh[1])
	ur, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("unable to parse url '%s'", rawURL)
	}
	req.Url = ur
	if req.Url.Scheme != "rtsp" {
		return nil, fmt.Errorf("invalid url scheme '%s'", req.Url.Scheme)
	}
	proto := string(rh[2])
	if proto != _RTSP_PROTO {
		return nil, fmt.Errorf("expected '%s', got '%s'", _RTSP_PROTO, proto)
	}

	req.Header, err = readHeaderFromString(string(hs[erhpos+2 : ehpos]))
	if err != nil {
		return nil, err
	}
	cl, _ := req.Header.getContentLength()
	if cl > 0 {
		if cl <= len(hs)-ehpos+4 {
			b := make([]byte, cl)
			copy(b, hs[ehpos+4:ehpos+4+cl])
			req.Content = b
		} else {
			return nil, fmt.Errorf("Not enough bytes to get all content")
		}
	}
	return req, nil
}

func readResponseFromBytes(hs []byte) (*Response, error) {
	res := &Response{}
	ehpos := bytes.Index(hs, []byte(END_LINE+END_LINE))
	if ehpos == -1 {
		return nil, fmt.Errorf("Could not find end of response to parse")
	}

	erhpos := bytes.Index(hs, []byte(END_LINE))
	rh := strings.SplitN(string(hs[:erhpos]), " ", 3)
	if len(rh) < 3 {
		return nil, fmt.Errorf("unable to parse status code")
	}
	statusCode64, err := strconv.ParseInt(rh[1], 10, 32)
	if err != nil {
		return nil, fmt.Errorf("unable to parse status code")
	}
	res.StatusCode = StatusCode(statusCode64)
	res.StatusMessage = rh[2]

	res.Header, err = readHeaderFromString(string(hs[erhpos+2 : ehpos]))
	if err != nil {
		return nil, err
	}
	cl, _ := res.Header.getContentLength()
	if cl > 0 {
		if cl <= len(hs)-ehpos+4 {
			b := make([]byte, cl)
			copy(b, hs[ehpos+4:ehpos+4+cl])
			res.Content = b
		} else {
			return nil, fmt.Errorf("Not enough bytes to get all content")
		}
	}
	return res, nil
}

func getContentLength(hs string) (int, error) {
	headers := strings.Split(hs, END_LINE)
	for _, hl := range headers {
		hkv := strings.Split(hl, ":")
		if strings.ToLower(hkv[0]) == "content-length" {
			cl, err := strconv.ParseInt(strings.TrimSpace(hkv[1]), 10, 64)
			if err != nil {
				return -1, err
			}
			return int(cl), nil
		}
	}
	return 0, nil
}

func readHeaderFromString(hs string) (Header, error) {
	h := make(Header)
	headers := strings.Split(hs, END_LINE)
	if len(headers) == 0 && len(hs) > 0 {
		return nil, fmt.Errorf("No headers found")
	}
	for _, hl := range headers {
		if hl == "" {
			continue
		}
		hkv := strings.SplitN(hl, ":", 2)
		key := normalizeHeaderKey(strings.TrimSpace(hkv[0]))
		val := strings.TrimSpace(hkv[1])
		if len(val) > 0 {
			h[key] = append(h[key], val)
		}
	}
	return h, nil
}
