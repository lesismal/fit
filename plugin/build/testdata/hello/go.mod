module github.com/lesismal/fit/plugin/build/testdata/hello

go 1.27.3

require (
	github.com/lesismal/fib v1.8.0
	github.com/lesismal/fit v0.0.0-00010101000000-000000000000
)

require golang.org/x/sys v0.49.0 // indirect

// Built from this checkout's fit; a module of your own requires a version
// of fit instead, or fit build replaces it.
replace github.com/lesismal/fit => ../../../..
