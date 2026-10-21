// Package standard imports every module fit ships with, so that importing it
// registers them all.
package standard

import (
	_ "github.com/lesismal/fit/handler/proxy"
	_ "github.com/lesismal/fit/handler/respond"
	_ "github.com/lesismal/fit/handler/static"
	_ "github.com/lesismal/fit/middleware/headers"
	_ "github.com/lesismal/fit/middleware/limit"
	_ "github.com/lesismal/fit/middleware/rewrite"
)
