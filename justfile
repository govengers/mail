set shell := ["bash", "-euo", "pipefail", "-c"]

# govengers/mail, from the library template of govengers/standards (templates/library).
# A library pins its tools here with go run, not with tool directives in go.mod (TCH-002).
# Three modules: the root, jobsadapter (depends on govengers/jobs) and cmd/mailgen (depends on the
# MJML engine); every recipe covers all three.

gotestsum := "go run gotest.tools/gotestsum@v1.13.0"
govulncheck := "go run golang.org/x/vuln/cmd/govulncheck@v1.9.0"
coverage_threshold := "80"
coverage_excluded := "/examples/|/test/"
e2e_inputs := "test/e2e testdata"
nested_modules := "jobsadapter cmd/mailgen"

default:
    @just --list

# Vet and lint every module with the govengers baseline (.golangci.yml, LIB-005 checks enabled); check the testdata HTML is what its MJML compiles to
lint:
    go vet ./...
    go vet -tags e2e ./test/e2e/...
    golangci-lint run ./...
    for module in {{nested_modules}}; do (cd "$module" && go vet ./... && golangci-lint run ./...); done
    go -C cmd/mailgen run . -dir ../../testdata/templates -check

# Compile the testdata MJML templates to HTML after changing them
mjml:
    go -C cmd/mailgen run . -dir ../../testdata/templates

# Unit tests of changed and affected packages; the Go test cache replays the rest (TST-014). Each module fails below the coverage threshold (TST-010); cmd/ is not gated
test *flags:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p sandbox
    root="{{justfile_directory()}}"
    {{gotestsum}} --format testname --jsonfile sandbox/root.json -- -race -coverprofile=sandbox/root.raw.cover {{flags}} ./...
    (cd jobsadapter && {{gotestsum}} --format testname --jsonfile "$root/sandbox/jobsadapter.json" -- -race -coverprofile="$root/sandbox/jobsadapter.raw.cover" {{flags}} ./...)
    (cd cmd/mailgen && {{gotestsum}} --format testname --jsonfile "$root/sandbox/mailgen.json" -- -race {{flags}} ./...)
    cat sandbox/root.json sandbox/jobsadapter.json sandbox/mailgen.json > sandbox/unit.json
    grep -v -E '{{coverage_excluded}}' sandbox/root.raw.cover > sandbox/unit.cover
    gate() {
        awk -v threshold={{coverage_threshold}} -v module="$1" '/^total:/ { found = 1; sub("%", "", $3); print module " coverage " $3 "%"; if ($3 + 0 < threshold) { print module " coverage is below " threshold "%"; exit 1 } } END { if (!found) { print "no coverage total"; exit 1 } }'
    }
    go tool cover -func=sandbox/unit.cover > sandbox/unit.func
    gate mail < sandbox/unit.func
    (cd jobsadapter && go tool cover -func="$root/sandbox/jobsadapter.raw.cover") | gate jobsadapter

# Every unit test, uncached and shuffled: the nightly run and before a release (TST-014, TST-005)
test-all: (test "-count=1" "-shuffle=on")

# Reproducible compiled suite and mailgen binary: their hash says whether the suite has anything new to test (TST-015)
e2e-build:
    mkdir -p sandbox/e2e-build
    go -C cmd/mailgen build -race -trimpath -buildvcs=false -o "{{justfile_directory()}}/sandbox/e2e-build/mailgen" .
    go test -c -race -tags e2e -trimpath -buildvcs=false -o sandbox/e2e-build/ ./test/e2e/...

# The end-to-end suite: mailgen compiles the templates, the transports deliver to protocol fakes (TST-011), always
test-e2e-all: e2e-build
    mkdir -p sandbox
    E2E_MAILGEN={{justfile_directory()}}/sandbox/e2e-build/mailgen {{gotestsum}} --format testname --jsonfile sandbox/e2e.json -- -race -tags e2e -trimpath -buildvcs=false -count=1 ./test/e2e/...

# The end-to-end suite, skipped when suite, binary and inputs equal a green run (TST-015); keeps sandbox/e2e.json for CI badges
test-e2e: e2e-build
    #!/usr/bin/env bash
    set -euo pipefail
    key=$(find sandbox/e2e-build {{e2e_inputs}} -type f -print0 | sort -z | xargs -0 shasum -a 256 | shasum -a 256 | cut -c1-32)
    passed="sandbox/e2e-passed/$key"
    if [ -f "$passed/e2e.json" ]; then
        echo "e2e skipped: suite, binary and inputs equal the green run $key"
        cp "$passed/e2e.json" sandbox/e2e.json
        exit 0
    fi
    "{{just_executable()}}" test-e2e-all
    mkdir -p "$passed"
    cp sandbox/e2e.json "$passed/e2e.json"

# Scan every module for known vulnerabilities in reachable code (TCH-008)
vulncheck:
    {{govulncheck}} ./...
    for module in {{nested_modules}}; do (cd "$module" && {{govulncheck}} ./...); done

# Everything CI runs on a change
check: lint test test-e2e vulncheck
