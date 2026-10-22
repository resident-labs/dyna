// Package realtime runs one speech-to-speech conversation with a realtime
// voice provider for Dyna's Voice. A Provider describes one service: where
// to dial, how to build its session and how its Protocol maps the wire
// events to Dyna's Events. Conn holds one open connection, and Call runs the
// conversation on it: the Go tools the model calls, the queue of what Voice
// must say for Dyna, turn-taking and barge-in.
//
// The provider contract, the GA protocol mapping and the tool loop bounds
// are ported from qwen-audio-agent (https://github.com/qwen-audio-agent),
// Copyright 2026 qwen-audio-agent contributors, under the Apache License,
// Version 2.0. See NOTICE at the repository root.
package realtime
