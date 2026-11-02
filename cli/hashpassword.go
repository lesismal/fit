package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

// stdin is what hash-password reads the password from.
var stdin io.Reader = os.Stdin

// rawTerminal puts stdin in raw mode when it is a terminal, for a password
// to be typed without the terminal's echo, and returns what puts it back;
// ok is false when stdin is not a terminal. A variable for tests to fake a
// terminal with.
var rawTerminal = func() (restore func(), ok bool, err error) {
	f, isFile := stdin.(*os.File)
	if !isFile || !term.IsTerminal(int(f.Fd())) {
		return nil, false, nil
	}
	old, err := term.MakeRaw(int(f.Fd()))
	if err != nil {
		return nil, true, err
	}
	return func() { _ = term.Restore(int(f.Fd()), old) }, true, nil
}

// errInterrupted is a password prompt left with Ctrl-C or Ctrl-D.
var errInterrupted = errors.New("interrupted")

// runHashPassword prints the bcrypt hash of a password, for basic_auth's
// password_hash. From a terminal the password is asked for twice, each key
// shown as a *; otherwise it is the first line of standard input. It is never
// taken as an argument, which other users of the machine could see.
func runHashPassword(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hash-password", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cost := fs.Int("cost", bcrypt.DefaultCost, "the bcrypt cost: each one more doubles the time a check takes")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "fit hash-password: the password is read from standard input, not taken as an argument")
		return 2
	}
	password, err := readPassword(stderr)
	if err != nil {
		fmt.Fprintf(stderr, "fit hash-password: %v\n", err)
		return 1
	}
	switch {
	case password == "":
		fmt.Fprintln(stderr, "fit hash-password: the password is empty")
		return 1
	case len(password) > 72:
		// bcrypt refuses longer ones rather than check only their start.
		fmt.Fprintln(stderr, "fit hash-password: bcrypt takes passwords of at most 72 bytes")
		return 1
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), *cost)
	if err != nil {
		fmt.Fprintf(stderr, "fit hash-password: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s\n", hash)
	return 0
}

// readPassword returns the password: asked for twice on prompt, masked,
// when stdin is a terminal, and the first line of stdin otherwise.
func readPassword(prompt io.Writer) (string, error) {
	restore, ok, err := rawTerminal()
	if err != nil {
		return "", err
	}
	if !ok {
		line, err := bufio.NewReader(io.LimitReader(stdin, 1024)).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	defer restore()
	in := bufio.NewReader(stdin)
	fmt.Fprint(prompt, "Password: ")
	password, err := readMasked(in, prompt)
	if err != nil || password == "" {
		return password, err
	}
	fmt.Fprint(prompt, "Confirm password: ")
	again, err := readMasked(in, prompt)
	if err != nil {
		return "", err
	}
	if again != password {
		return "", errors.New("the passwords do not match")
	}
	return password, nil
}

// readMasked reads a line from a terminal in raw mode, writing a * to echo
// for each character typed and taking back the last one on Backspace and all
// of them on Ctrl-U. Escape sequences, as arrow keys send, and other control
// characters are dropped.
func readMasked(in *bufio.Reader, echo io.Writer) (string, error) {
	var b []byte
	defer fmt.Fprint(echo, "\r\n") // raw mode does not turn \n into \r\n
	for {
		c, err := in.ReadByte()
		if err != nil {
			if err == io.EOF {
				err = errInterrupted
			}
			return "", err
		}
		switch {
		case c == '\r' || c == '\n':
			return string(b), nil
		case c == 3 || c == 4: // Ctrl-C, Ctrl-D
			return "", errInterrupted
		case c == 8 || c == 127: // Backspace
			if len(b) > 0 {
				_, n := utf8.DecodeLastRune(b)
				b = b[:len(b)-n]
				fmt.Fprint(echo, "\b \b")
			}
		case c == 21: // Ctrl-U
			fmt.Fprint(echo, strings.Repeat("\b \b", utf8.RuneCount(b)))
			b = b[:0]
		case c == 27: // ESC: drop the sequence it starts
			if next, err := in.ReadByte(); err == nil && (next == '[' || next == 'O') {
				for {
					if f, err := in.ReadByte(); err != nil || f >= 0x40 && f <= 0x7e {
						break
					}
				}
			}
		case c < 32:
		case len(b) >= 1024:
			// Far past bcrypt's 72; kept from growing without end.
		default:
			b = append(b, c)
			if !utf8.RuneStart(c) {
				continue // a rune's later bytes, its * written
			}
			fmt.Fprint(echo, "*")
		}
	}
}
