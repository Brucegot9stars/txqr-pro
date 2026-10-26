module github.com/divan/txqr

go 1.25.12

require (
	github.com/google/gofountain v0.0.0-20160820054803-4928733085e9
	github.com/makiuchi-d/gozxing v0.0.0-20181111004219-a01698524e72
	github.com/mdp/qrterminal v1.0.1
	github.com/pyk/byten v0.0.0-20140925233358-f847a130bf6d
	github.com/skip2/go-qrcode v0.0.0-20171229120447-cf5f9fa2f0d8
	github.com/xssnick/raptorq v1.4.0
	golang.org/x/sys v0.47.0
)

replace github.com/skip2/go-qrcode => ./third_party/go-qrcode

require (
	golang.org/x/text v0.3.0 // indirect
	rsc.io/qr v0.2.0 // indirect
)
