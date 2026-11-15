module github.com/govengers/mail/jobsadapter

go 1.26.0

require (
	github.com/google/uuid v1.6.0
	github.com/govengers/jobs v1.1.0
	github.com/govengers/mail v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.12.1
)

require (
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sync v0.24.0 // indirect
)

// The adapter is developed against the mail module of the same commit; a release tags the root
// module first and requires that version here.
replace github.com/govengers/mail => ../
