package gortsplib

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

const (
	_MAX_HEADER_COUNT        = 255
	_MAX_HEADER_KEY_LENGTH   = 1024
	_MAX_HEADER_VALUE_LENGTH = 1024
)

func normalizeHeaderKey(in string) string {
	switch strings.ToLower(in) {
	case "rtp-info":
		return "RTP-INFO"

	case "www-authenticate":
		return "WWW-Authenticate"

	case "cseq":
		return "CSeq"
	}
	return http.CanonicalHeaderKey(in)
}

// Header is a RTSP reader, present in both Requests and Responses.
type Header map[string][]string

func (h *Header) setContentLength(cl int) {
	hd := *h
	hd["Content-Length"] = []string{strconv.FormatInt(int64(cl), 10)}
}

func (h *Header) getContentLength() (int, error) {
	hd := *h
	cls, ok := hd["Content-Length"]
	if !ok || len(cls) != 1 {
		return 0, nil
	}

	cl, err := strconv.ParseInt(cls[0], 10, 64)
	if err != nil {
		return -1, err
	}
	return int(cl), nil
}

func (h *Header) String() string {
	hd := *h
	var keys []string
	for key := range hd {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	sb := strings.Builder{}

	for _, k := range keys {
		v := hd[k]
		for _, v2 := range v {
			sb.WriteString(k)
			sb.WriteString(": ")
			sb.WriteString(v2)
			sb.WriteString(END_LINE)
		}
	}
	return sb.String()
}
