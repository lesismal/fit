package cli

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// stdin is what hash-password reads the password from.
var stdin io.Reader = os.Stdin

// runHashPassword prints the bcrypt hash of the password on the first line
// of standard input, for basic_auth's password_hash. The password is not
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
	line, err := bufio.NewReader(io.LimitReader(stdin, 1024)).ReadString('\n')
	if err != nil && err != io.EOF {
		fmt.Fprintf(stderr, "fit hash-password: %v\n", err)
		return 1
	}
	password := strings.TrimRight(line, "\r\n")
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
