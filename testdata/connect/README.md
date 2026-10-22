These fixed vectors cover the Connect wire format in Go and TypeScript.
Both test suites read protocol.json. The keys are public test data.
The machine seed comes from RFC 8032 test 1. The relay seed is bytes 0 to 31.
The signatures use Ed25519. Hashes use SHA-256 with unpadded base64url.
The timestamp is fixed at 1700000000. These requests are not live credentials.
