module github.com/lesismal/fit/examples/module/hello

go 1.27

require (
	github.com/lesismal/fib v1.5.7-0.20261022203111-39989533638b
	github.com/lesismal/fit v0.0.0-00010101000000-000000000000
)

// Built from this checkout's fit; a module of your own requires a version
// of fit instead, or fit build replaces it.
replace github.com/lesismal/fit => ../../..
