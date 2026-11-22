module github.com/rendobar/rendobar-go/examples

go 1.23

require github.com/rendobar/rendobar-go v0.0.0

require github.com/google/uuid v1.6.0 // indirect

// The examples build against this checkout, the way a user's module builds against a release.
replace github.com/rendobar/rendobar-go => ../
