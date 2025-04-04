// Copyright © 2019 - 2022 Dell Inc. or its subsidiaries. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//      http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/dell/goscaleio/log"
)

func isBinOctetBody(h http.Header) bool {
	return h.Get(HeaderKeyContentType) == headerValContentTypeBinaryOctetStream
}

func logRequest(
	_ context.Context,
	req *http.Request,
	lf func(func(msg string, args ...any), string),
) {
	w := &bytes.Buffer{}

	fmt.Fprintln(w)
	fmt.Fprint(w, "    -------------------------- ")
	fmt.Fprint(w, "GOSCALEIO HTTP REQUEST")
	fmt.Fprintln(w, " -------------------------")

	buf, err := dumpRequest(req, !isBinOctetBody(req.Header))
	if err != nil {
		return
	}

	if err := WriteIndented(w, buf); err != nil {
		fmt.Printf("WriteIndented returned error: %s", err.Error())
	}
	if lf != nil {
		lf(log.Log.Debug, w.String())
	}
}

func logResponse(
	_ context.Context,
	res *http.Response,
	lf func(func(msg string, args ...any), string),
) {
	w := &bytes.Buffer{}

	fmt.Fprintln(w)
	fmt.Fprint(w, "    -------------------------- ")
	fmt.Fprint(w, "GOSCALEIO HTTP RESPONSE")
	fmt.Fprintln(w, " -------------------------")

	buf, err := httputil.DumpResponse(res, !isBinOctetBody(res.Header))
	if err != nil {
		return
	}

	bw := &bytes.Buffer{}

	if err := WriteIndented(w, buf); err != nil {
		fmt.Printf("WriteIndented returned error: %s", err.Error())
	}

	scanner := bufio.NewScanner(bw)
	for {
		if !scanner.Scan() {
			break
		}
		fmt.Fprintln(w, scanner.Text())
	}
	if lf != nil {
		lf(log.Log.Debug, w.String())
	}
}

// WriteIndentedN indents all lines n spaces.
func WriteIndentedN(w io.Writer, b []byte, n int) error {
	s := bufio.NewScanner(bytes.NewReader(b))
	if !s.Scan() {
		return nil
	}
	l := s.Text()
	for {
		for x := 0; x < n; x++ {
			if _, err := fmt.Fprint(w, " "); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprint(w, l); err != nil {
			return err
		}
		if !s.Scan() {
			break
		}
		l = s.Text()
		if _, err := fmt.Fprint(w, "\n"); err != nil {
			return err
		}
	}
	return nil
}

// WriteIndented indents all lines four spaces.
func WriteIndented(w io.Writer, b []byte) error {
	return WriteIndentedN(w, b, 4)
}

var reqWriteExcludeHeaderDump = map[string]bool{
	"Host":              true, // not in Header map anyway
	"Transfer-Encoding": true,
	"Trailer":           true,
	"Authorization":     true,
}

func drainBody(b io.ReadCloser) (r1, r2 io.ReadCloser, err error) {
	if b == http.NoBody {
		// No copying needed. Preserve the magic sentinel meaning of NoBody.
		return http.NoBody, http.NoBody, nil
	}
	var buf bytes.Buffer
	if _, err = buf.ReadFrom(b); err != nil {
		return nil, b, err
	}
	if err = b.Close(); err != nil {
		return nil, b, err
	}
	return io.NopCloser(&buf), io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}

func dumpRequest(req *http.Request, body bool) ([]byte, error) {
	var err error
	save := req.Body
	if !body || req.Body == nil {
		req.Body = nil
	} else {
		save, req.Body, err = drainBody(req.Body)
		if err != nil {
			return nil, err
		}
	}

	var b bytes.Buffer

	// By default, print out the unmodified req.RequestURI, which
	// is always set for incoming server requests. But because we
	// previously used req.URL.RequestURI and the docs weren't
	// always so clear about when to use DumpRequest vs
	// DumpRequestOut, fall back to the old way if the caller
	// provides a non-server Request.
	reqURI := req.RequestURI
	if reqURI == "" {
		reqURI = req.URL.RequestURI()
	}

	method := http.MethodGet
	if req.Method != "" {
		method = req.Method
	}

	fmt.Fprintf(&b, "%s %s HTTP/%d.%d\r\n", method, reqURI, req.ProtoMajor, req.ProtoMinor)

	absRequestURI := strings.HasPrefix(req.RequestURI, "http://") || strings.HasPrefix(req.RequestURI, "https://")
	if !absRequestURI {
		host := req.Host
		if host == "" && req.URL != nil {
			host = req.URL.Host
		}
		if host != "" {
			fmt.Fprintf(&b, "Host: %s\r\n", host)
		}
	}

	chunked := len(req.TransferEncoding) > 0 && req.TransferEncoding[0] == "chunked"
	if len(req.TransferEncoding) > 0 {
		fmt.Fprintf(&b, "Transfer-Encoding: %s\r\n", strings.Join(req.TransferEncoding, ","))
	}
	if req.Close {
		fmt.Fprintf(&b, "Connection: close\r\n")
	}

	err = req.Header.WriteSubset(&b, reqWriteExcludeHeaderDump)
	if err != nil {
		return nil, err
	}

	if _, err := io.WriteString(&b, "\r\n"); err != nil {
		fmt.Printf("io.WriteString returned error: %s", err.Error())
	}

	if req.Body != nil {
		var dest io.Writer = &b
		if chunked {
			dest = httputil.NewChunkedWriter(dest)
		}
		_, err = io.Copy(dest, req.Body)
		if chunked {
			if err := dest.(io.Closer).Close(); err != nil {
				fmt.Printf("io.Closer.Close() returned error: %s", err.Error())
			}

			if _, err := io.WriteString(&b, "\r\n"); err != nil {
				fmt.Printf("io.WriteString returned error: %s", err.Error())
			}

		}
	}

	req.Body = save
	if err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
