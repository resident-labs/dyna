module dyna-agent/cmd/dyna-export

go 1.26.10

require (
	dyna-agent v0.0.0
	github.com/resident-labs/dyna v0.0.0
)

replace dyna-agent => ../..

replace github.com/resident-labs/dyna => ../../next
