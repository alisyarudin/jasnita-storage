package sigv4

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"strconv"
	"strings"
)

// ChunkedReader membongkar body ber-encoding aws-chunked (Content-Encoding: aws-chunked),
// baik yang tiap chunk-nya bertanda tangan (STREAMING-AWS4-HMAC-SHA256-PAYLOAD[-TRAILER])
// maupun tanpa tanda tangan (STREAMING-UNSIGNED-PAYLOAD-TRAILER). Trailer dibaca lalu diabaikan.
type ChunkedReader struct {
	br        *bufio.Reader
	res       *Result
	signed    bool
	prevSig   string
	remaining int64 // sisa byte chunk saat ini
	chunkHash hash.Hash
	chunkSig  string
	eof       bool
	err       error
}

// NewChunkedReader membuat reader; res wajib dari Verify agar signing key/seed signature tersedia.
func NewChunkedReader(body io.Reader, res *Result) *ChunkedReader {
	signed := res.ContentSHA256 == StreamingSignedPayload || res.ContentSHA256 == StreamingSignedTrailer
	return &ChunkedReader{br: bufio.NewReaderSize(body, 64*1024), res: res, signed: signed, prevSig: res.Signature}
}

// IsStreaming melaporkan apakah nilai x-amz-content-sha256 menandakan body aws-chunked.
func IsStreaming(contentSHA string) bool {
	switch contentSHA {
	case StreamingSignedPayload, StreamingSignedTrailer, StreamingUnsignedTrailer:
		return true
	}
	return false
}

func (c *ChunkedReader) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	if c.eof {
		return 0, io.EOF
	}
	if c.remaining == 0 {
		if err := c.readChunkHeader(); err != nil {
			c.err = err
			return 0, err
		}
		if c.eof {
			return 0, io.EOF
		}
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.br.Read(p)
	c.remaining -= int64(n)
	if c.signed {
		c.chunkHash.Write(p[:n])
	}
	if c.remaining == 0 {
		if err2 := c.finishChunk(); err2 != nil {
			c.err = err2
			return n, err2
		}
	}
	if err == io.EOF && c.remaining > 0 {
		err = io.ErrUnexpectedEOF
	}
	if err == io.EOF {
		err = nil
	}
	return n, err
}

func (c *ChunkedReader) readLine() (string, error) {
	line, err := c.br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (c *ChunkedReader) readChunkHeader() error {
	line, err := c.readLine()
	if err != nil {
		return fmt.Errorf("aws-chunked: header chunk: %w", err)
	}
	sizeStr, rest, _ := strings.Cut(line, ";")
	size, err := strconv.ParseInt(strings.TrimSpace(sizeStr), 16, 64)
	if err != nil || size < 0 {
		return fmt.Errorf("aws-chunked: ukuran chunk tidak valid: %q", line)
	}
	c.chunkSig = ""
	if c.signed {
		for _, kv := range strings.Split(rest, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok && k == "chunk-signature" {
				c.chunkSig = v
			}
		}
		if c.chunkSig == "" {
			return fmt.Errorf("aws-chunked: chunk-signature tidak ada")
		}
		c.chunkHash = sha256.New()
	}
	c.remaining = size
	if size == 0 {
		// chunk terakhir: verifikasi lalu baca trailer sampai baris kosong / EOF
		if c.signed {
			if err := c.verifyChunk(); err != nil {
				return err
			}
		}
		for {
			l, err := c.readLine()
			if err != nil || l == "" {
				break
			}
		}
		c.eof = true
	}
	return nil
}

func (c *ChunkedReader) finishChunk() error {
	// setiap chunk diakhiri CRLF
	crlf := make([]byte, 2)
	if _, err := io.ReadFull(c.br, crlf); err != nil {
		return fmt.Errorf("aws-chunked: akhir chunk: %w", err)
	}
	if c.signed {
		return c.verifyChunk()
	}
	return nil
}

func (c *ChunkedReader) verifyChunk() error {
	sts := Algorithm + "-PAYLOAD\n" + c.res.Date.Format(amzDateFormat) + "\n" + c.res.Scope + "\n" +
		c.prevSig + "\n" + EmptySHA256 + "\n" + hex.EncodeToString(c.chunkHash.Sum(nil))
	expected := hex.EncodeToString(hmacSHA256(c.res.SigningKey, sts))
	if expected != c.chunkSig {
		return ErrChunkSignature
	}
	c.prevSig = expected
	return nil
}
