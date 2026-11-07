//go:build pgoverlay

package pgoverlay_test

import (
	"encoding/binary"
	"errors"
	"io"
)

const maxProtocolMessage = 16 << 20

func protocolStartup(reader io.Reader) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint32(header))
	if size < 4 || size > maxProtocolMessage {
		return nil, errors.New("invalid PostgreSQL startup message length")
	}
	message := make([]byte, size)
	copy(message, header)
	_, err := io.ReadFull(reader, message[4:])
	return message, err
}

func protocolMessage(reader io.Reader) ([]byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint32(header[1:]))
	if size < 4 || size > maxProtocolMessage {
		return nil, errors.New("invalid PostgreSQL message length")
	}
	message := make([]byte, size+1)
	copy(message, header)
	_, err := io.ReadFull(reader, message[5:])
	return message, err
}
