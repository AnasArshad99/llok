package guest

import (
	"bytes"
	"compress/gzip"
	"io"
)

// gunzipAll decompresses a (possibly multi-member) gzip stream, returning
// whatever it managed to read; good enough for scanning an APKINDEX.
func gunzipAll(data []byte) []byte {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	r.Multistream(true)
	out, _ := io.ReadAll(r)
	return out
}
