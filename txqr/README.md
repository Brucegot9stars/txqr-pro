# TXQR

[![GoDoc](https://godoc.org/github.com/divan/txqr?status.svg)](https://godoc.org/github.com/divan/txqr)

TXQR (Transfer via QR) is a protocol and set of tools and libs to transfer data via animated QR codes. It uses [fountain codes](https://en.wikipedia.org/wiki/Fountain_code) for error correction.

See related blog posts for more details:
 - [Animated QR data transfer with Gomobile and Gopherjs](https://divan.github.io/posts/animatedqr/)
 - [Fountain codes and animated QR](https://divan.github.io/posts/fountaincodes/)

# Demo

Demo animation and the automated tester app were removed during repository
cleanup (see `../docs/cleanup-analysis.md`). The production sender lives in
`cmd/txqr-web` and the desktop receiver in `cmd/txqr-desktop`.

Reader iOS app in the original demo (uses this lib via Gomobile): [https://github.com/divan/txqr-reader](https://github.com/divan/txqr-reader)

# Licence

MIT
