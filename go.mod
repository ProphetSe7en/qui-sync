module github.com/prophetse7en/qui-sync

go 1.26.0

// The toolchain qui-sync is built and tested with. The go directive above
// is the minimum a consumer needs; this is what we actually use, and it
// matters because the standard library ships security fixes in patch
// releases. Bump together with the Dockerfile builder image and the Go
// version in .github/workflows/ci.yml.
toolchain go1.27.4

require (
	github.com/evanphx/json-patch/v5 v5.9.11
	github.com/robfig/cron/v3 v3.0.1
	github.com/wI2L/jsondiff v0.7.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
)

require (
	golang.org/x/crypto v0.59.0
	golang.org/x/sys v0.50.0 // indirect
)
