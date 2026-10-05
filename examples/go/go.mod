module github.com/ubag/ubag-examples

go 1.21

require github.com/ubag/ubag-go v0.0.0

// Local checkout; in your own module `go get` the published SDK instead.
replace github.com/ubag/ubag-go => ../../packages/sdk-go
