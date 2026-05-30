package protocodec

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"
)

// Codec handles Protocol Buffer encoding/decoding with length prefix
type Codec struct{}

// NewCodec creates a new Protocol Buffer codec
func NewCodec() *Codec {
	return &Codec{}
}

// Encode encodes a protobuf message with length prefix
// Format: [4-byte length][protobuf data]
func (c *Codec) Encode(msg proto.Message) ([]byte, error) {
	data, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal proto: %w", err)
	}

	buf := new(bytes.Buffer)

	// Write length prefix (4 bytes, big endian)
	length := uint32(len(data))
	if err := binary.Write(buf, binary.BigEndian, length); err != nil {
		return nil, fmt.Errorf("failed to write length: %w", err)
	}

	// Write protobuf data
	if _, err := buf.Write(data); err != nil {
		return nil, fmt.Errorf("failed to write data: %w", err)
	}

	return buf.Bytes(), nil
}

// Decode decodes a protobuf message with length prefix
func (c *Codec) Decode(r io.Reader, msg proto.Message) error {
	// Read length prefix (4 bytes)
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return fmt.Errorf("failed to read length: %w", err)
	}

	// Read protobuf data
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return fmt.Errorf("failed to read data: %w", err)
	}

	// Unmarshal protobuf
	if err := proto.Unmarshal(data, msg); err != nil {
		return fmt.Errorf("failed to unmarshal proto: %w", err)
	}

	return nil
}

// EncodeStream encodes and writes a message to a writer
func (c *Codec) EncodeStream(w io.Writer, msg proto.Message) error {
	data, err := c.Encode(msg)
	if err != nil {
		return err
	}

	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("failed to write to stream: %w", err)
	}

	return nil
}
