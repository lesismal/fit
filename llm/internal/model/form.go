package model

import (
	"bytes"
	"errors"
	"mime"
	"strings"
)

// MaxBoundary is the longest multipart boundary RFC 2046 allows.
// MaxBoundary RFC 2046 允许的最长 multipart boundary。
const MaxBoundary = 70

// FormBoundary returns the boundary of a multipart/form-data media type, or "" when ct is not one or its boundary
// is missing or too long.
// FormBoundary 返回 multipart/form-data 媒体类型的 boundary；ct 不是这种类型，或 boundary 缺失、过长时返回 ""。
func FormBoundary(ct string) string {
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil || mt != "multipart/form-data" {
		return ""
	}
	b := params["boundary"]
	if b == "" || len(b) > MaxBoundary {
		return ""
	}
	return b
}

// FormField finds the value of the first part named name of a multipart/form-data body: it is body[start:end]. It
// reads the bytes in place, so that a caller may read the field, or replace it and keep every other byte, files
// included, as it is.
// FormField 查找 multipart/form-data 请求体中第一个名为 name 的部分的值：即 body[start:end]。它原地读取字节，调用方可以
// 读取该字段，或替换它而其余字节（包括文件）原样保留。
func FormField(body []byte, boundary, name string) (start, end int, err error) {
	dash := []byte("--" + boundary)
	delim := append([]byte("\r\n"), dash...)
	i := bytes.Index(body, dash)
	if i < 0 || (i > 0 && (i < 2 || !bytes.Equal(body[i-2:i], []byte("\r\n")))) {
		return 0, 0, errors.New("the body is not multipart/form-data of its boundary")
	}
	pos := i + len(dash)
	for {
		for pos < len(body) && (body[pos] == ' ' || body[pos] == '\t') {
			pos++ // transport padding
		}
		if bytes.HasPrefix(body[pos:], []byte("--")) {
			return 0, 0, errors.New("no " + name + " field in the form")
		}
		if !bytes.HasPrefix(body[pos:], []byte("\r\n")) {
			return 0, 0, errors.New("a malformed multipart boundary line")
		}
		hdrStart := pos + 2
		hdrLen := bytes.Index(body[hdrStart:], []byte("\r\n\r\n"))
		if hdrLen < 0 {
			return 0, 0, errors.New("a multipart part without the end of its header")
		}
		contentStart := hdrStart + hdrLen + 4
		contentEnd := nextDelim(body, contentStart, delim)
		if contentEnd < 0 {
			return 0, 0, errors.New("a multipart part without its closing boundary")
		}
		if partName(body[hdrStart:hdrStart+hdrLen]) == name {
			return contentStart, contentEnd, nil
		}
		pos = contentEnd + len(delim)
	}
}

// nextDelim returns where the next delimiter, CRLF--boundary, starts at or
// after from: one followed by CRLF, -- or transport padding, as content may
// hold the delimiter's bytes followed by others.
func nextDelim(body []byte, from int, delim []byte) int {
	for {
		n := bytes.Index(body[from:], delim)
		if n < 0 {
			return -1
		}
		at := from + n
		rest := bytes.TrimLeft(body[at+len(delim):], " \t")
		if bytes.HasPrefix(rest, []byte("\r\n")) || bytes.HasPrefix(rest, []byte("--")) {
			return at
		}
		from = at + 1
	}
}

// partName returns the form name of a part's header, from its Content-Disposition.
func partName(hdr []byte) string {
	for _, line := range strings.Split(string(hdr), "\r\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "Content-Disposition") {
			continue
		}
		disp, params, err := mime.ParseMediaType(strings.TrimSpace(v))
		if err != nil || disp != "form-data" {
			return ""
		}
		return params["name"]
	}
	return ""
}
