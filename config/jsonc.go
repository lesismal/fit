package config

import "bytes"

// Standardize returns data, a config in JSON with comments and trailing
// commas, as standard JSON: each // line comment and /* block comment */
// becomes spaces, but for the line breaks in it, and so does a comma
// before the ] or } that closes an array or object. Every byte keeps its
// offset, so a position in the result is the same line and column in data.
// data is returned as it is when it has neither; it is never changed.
func Standardize(data []byte) ([]byte, error) {
	if bytes.IndexByte(data, '/') < 0 && !trailingComma(data) {
		return data, nil
	}
	out := bytes.Clone(data)
	inString := false
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inString {
			switch c {
			case '\\':
				i++
			case '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
		case c == '/' && i+1 < len(out) && out[i+1] == '/':
			for ; i < len(out) && out[i] != '\n'; i++ {
				out[i] = ' '
			}
		case c == '/' && i+1 < len(out) && out[i+1] == '*':
			end := bytes.Index(out[i+2:], []byte("*/"))
			if end < 0 {
				line, col := (&source{data: data}).pos(i)
				return nil, &Error{Line: line, Col: col, Msg: "invalid JSON: a /* comment that does not end"}
			}
			for j := i; j < i+2+end+2; j++ {
				if out[j] != '\n' && out[j] != '\r' {
					out[j] = ' '
				}
			}
			i += 2 + end + 1
		case c == ']' || c == '}':
			// A comma after a value only: [,] stays an error.
			if j := lastToken(out, i); j >= 0 && out[j] == ',' {
				if k := lastToken(out, j); k >= 0 && out[k] != '[' && out[k] != '{' && out[k] != ',' {
					out[j] = ' '
				}
			}
		}
	}
	return out, nil
}

// lastToken returns the index of the last byte before i that is not white
// space, or -1.
func lastToken(b []byte, i int) int {
	for j := i - 1; j >= 0; j-- {
		switch b[j] {
		case ' ', '\t', '\r', '\n':
		default:
			return j
		}
	}
	return -1
}

// trailingComma reports whether data may have a comma before a closing
// bracket or brace, outside strings or not: a quick test that lets most
// standard configs through untouched.
func trailingComma(data []byte) bool {
	for i, c := range data {
		if c == ']' || c == '}' {
			if j := lastToken(data, i); j >= 0 && data[j] == ',' {
				return true
			}
		}
	}
	return false
}
