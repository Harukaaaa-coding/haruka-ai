package image

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	stdimage "image"
	"image/png"
	"testing"
)

func TestValidateUploadAcceptsPNG(t *testing.T) {
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, stdimage.NewNRGBA(stdimage.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	if err := ValidateUpload(buffer.Bytes()); err != nil {
		t.Fatalf("ValidateUpload returned an error: %v", err)
	}
}

func TestValidateUploadRejectsEmptyUnsupportedAndOversizedData(t *testing.T) {
	tests := []struct {
		name   string
		buffer []byte
		want   error
	}{
		{name: "empty", buffer: nil, want: ErrEmptyImage},
		{name: "unsupported", buffer: []byte("not an image"), want: ErrUnsupportedImage},
		{name: "encoded size", buffer: make([]byte, MaxUploadBytes+1), want: ErrImageTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateUpload(test.buffer); !errors.Is(err, test.want) {
				t.Fatalf("ValidateUpload error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestValidateUploadRejectsAbnormalPixelDimensions(t *testing.T) {
	tests := []struct {
		name          string
		width, height uint32
	}{
		{name: "too many pixels", width: 6000, height: 5000},
		{name: "dimension too large", width: MaxImageDimension + 1, height: 1},
		{name: "extreme aspect ratio", width: 1000, height: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateUpload(pngHeader(test.width, test.height))
			if !errors.Is(err, ErrImageDimensions) {
				t.Fatalf("ValidateUpload error = %v, want ErrImageDimensions", err)
			}
		})
	}
}

func pngHeader(width, height uint32) []byte {
	var buffer bytes.Buffer
	buffer.Write([]byte{137, 80, 78, 71, 13, 10, 26, 10})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8] = 8
	ihdr[9] = 2
	writePNGChunk(&buffer, "IHDR", ihdr)
	writePNGChunk(&buffer, "IEND", nil)
	return buffer.Bytes()
}

func writePNGChunk(buffer *bytes.Buffer, name string, contents []byte) {
	_ = binary.Write(buffer, binary.BigEndian, uint32(len(contents)))
	chunkType := []byte(name)
	buffer.Write(chunkType)
	buffer.Write(contents)
	checksumData := append(append([]byte{}, chunkType...), contents...)
	_ = binary.Write(buffer, binary.BigEndian, crc32.ChecksumIEEE(checksumData))
}
