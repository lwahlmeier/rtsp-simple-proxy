package gortsplib

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	_INTERLEAVED_FRAME_MAX_SIZE         = 512 * 1024
	_INTERLEAVED_FRAME_MAX_CONTENT_SIZE = (_INTERLEAVED_FRAME_MAX_SIZE - 4)
)

// InterleavedFrame is a structure that allows to send and receive binary data
// with RTSP connections.
// It is usually used to send RTP and RTCP with RTSP.
type InterleavedFrame struct {
	Channel uint8
	Content []byte
}

func readInterleavedFrame(r io.Reader) (*InterleavedFrame, error) {
	var header [4]byte
	_, err := io.ReadFull(r, header[:])
	if err != nil {
		return nil, err
	}

	if header[0] != 0x24 {
		return nil, fmt.Errorf("wrong magic byte (0x%.2x)", header[0])
	}

	framelen := int(binary.BigEndian.Uint16(header[2:]))
	if framelen > _INTERLEAVED_FRAME_MAX_SIZE {
		return nil, fmt.Errorf("frame length greater than maximum allowed (%d vs %d)",
			framelen, _INTERLEAVED_FRAME_MAX_SIZE)
	}

	f := &InterleavedFrame{
		Channel: header[1],
		Content: make([]byte, framelen),
	}

	_, err = io.ReadFull(r, f.Content)
	if err != nil {
		return nil, err
	}

	return f, nil
}

func (f *InterleavedFrame) write(bw io.Writer) error {
	bh := make([]byte, 4+len(f.Content))
	bh[0] = 0x24
	bh[1] = f.Channel
	binary.BigEndian.PutUint16(bh[2:4], uint16(len(f.Content)))
	copy(bh[4:], f.Content)

	_, err := bw.Write(bh)
	if err != nil {
		return err
	}

	return nil
}
