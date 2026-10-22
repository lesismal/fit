package cli

// The tests serve configs of every module and provider fit ships with, as
// fit's own main package has them.
import (
	_ "github.com/lesismal/fit/modules/standard"
	_ "github.com/lesismal/fit/provider/standard"
)
