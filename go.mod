module github.com/hbaldwin98/control-center

go 1.25.0

require (
	github.com/google/cel-go v0.26.1
	github.com/hbaldwin98/control-center/host v0.0.0
	github.com/hbaldwin98/control-center/plugins/hello v0.0.0
	github.com/mxschmitt/playwright-go v0.6201.1
	golang.org/x/crypto v0.55.0
	golang.org/x/net v0.58.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/libc v1.74.4
	modernc.org/sqlite v1.57.0
)

require (
	cel.dev/expr v0.24.0 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.0 // indirect
	github.com/deckarep/golang-set/v2 v2.8.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-stack/stack v1.8.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/stoewer/go-strcase v1.2.0 // indirect
	golang.org/x/exp v0.0.0-20230515195305-f3d0a9c9a5cc // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20240826202546-f6391c0de4c7 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240826202546-f6391c0de4c7 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

replace github.com/hbaldwin98/control-center/host => ./host

replace github.com/hbaldwin98/control-center/plugins/hello => ./plugins/hello
