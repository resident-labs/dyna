package terminalsvc

// ReadLogChunk lets chunk sweeps skip ShellLogRange's job row lookup, which
// costs milliseconds per call under -race.
var ReadLogChunk = readLogChunk
