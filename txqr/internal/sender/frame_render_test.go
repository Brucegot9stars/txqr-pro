package sender

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"testing"

	"github.com/divan/txqr/qr"
)

// frameFor builds a frame with the header shape the server emits, so the symbol
// geometry matches a real transfer: "block/chunk/total/codec|data".
func frameFor(seed, chunk, total int) []byte {
	r := rand.New(rand.NewSource(int64(seed)))
	body := make([]byte, chunk)
	r.Read(body)
	return []byte(fmt.Sprintf("%d/%d/%d/raptorq|%s", seed%97, chunk, total, body))
}

// smoothRescale models what the browser now does to the frame image: bilinear
// scaling to min(92vw,90vh). Until v1.4.4 the page asked for
// `image-rendering: pixelated` instead, i.e. nearest-neighbour, which drops
// whole module rows whenever the window is slightly smaller than the image.
func smoothRescale(src image.Image, w, h int) *image.RGBA {
	sb := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	at := func(x, y int) (float64, float64, float64) {
		if x < 0 {
			x = 0
		}
		if y < 0 {
			y = 0
		}
		if x >= sb.Dx() {
			x = sb.Dx() - 1
		}
		if y >= sb.Dy() {
			y = sb.Dy() - 1
		}
		r, g, b, _ := src.At(sb.Min.X+x, sb.Min.Y+y).RGBA()
		return float64(r >> 8), float64(g >> 8), float64(b >> 8)
	}
	for y := 0; y < h; y++ {
		fy := math.Max((float64(y)+0.5)*float64(sb.Dy())/float64(h)-0.5, 0)
		y0 := int(fy)
		ty := fy - float64(y0)
		for x := 0; x < w; x++ {
			fx := math.Max((float64(x)+0.5)*float64(sb.Dx())/float64(w)-0.5, 0)
			x0 := int(fx)
			tx := fx - float64(x0)
			var mix [3]float64
			for _, c := range []struct {
				dx, dy int
				w      float64
			}{
				{0, 0, (1 - tx) * (1 - ty)},
				{1, 0, tx * (1 - ty)},
				{0, 1, (1 - tx) * ty},
				{1, 1, tx * ty},
			} {
				r, g, b := at(x0+c.dx, y0+c.dy)
				mix[0] += r * c.w
				mix[1] += g * c.w
				mix[2] += b * c.w
			}
			dst.Set(x, y, color.RGBA{uint8(mix[0]), uint8(mix[1]), uint8(mix[2]), 255})
		}
	}
	return dst
}

// captureRegion places the displayed code on the page background, which is what
// a drawn region around the sender's frame area contains.
func captureRegion(code image.Image, margin int) *image.RGBA {
	b := code.Bounds()
	side := b.Dx() + 2*margin
	out := image.NewRGBA(image.Rect(0, 0, side, side))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			out.Set(x, y, color.RGBA{255, 255, 255, 255}) // the page's white card
		}
	}
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.Set(margin+x, margin+y, code.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

// TestPageDoesNotAskForNearestNeighbourScaling guards the one-line CSS change
// that took the measured decode rate from 56% to 91%. Nearest-neighbour looks
// tidier but is lossy: scaling a 512 px image down to 500 px drops every 25th
// module row, and a barcode detector then sees a symbol with missing rows.
func TestPageDoesNotAskForNearestNeighbourScaling(t *testing.T) {
	// Strip /* */ comments first: the file explains the decision in prose and
	// that prose mentions the very property being banned.
	page := string(indexHTML)
	for {
		start := strings.Index(page, "/*")
		if start < 0 {
			break
		}
		end := strings.Index(page[start:], "*/")
		if end < 0 {
			break
		}
		page = page[:start] + page[start+end+2:]
	}
	if strings.Contains(page, "image-rendering") {
		t.Error("index.html sets image-rendering again: `pixelated` breaks frame decoding " +
			"whenever the browser window is smaller than the frame image (measured 56% vs 91% decode rate)")
	}
}

// TestFramesDecodeThroughBrowserRescale is the regression test for the shipped
// combination (512 px frame image + smooth browser scaling). It walks the window
// sizes a user actually produces; the rate is asserted rather than every single
// size because resampling phase makes individual sizes a coin flip, while a
// transfer only needs most frames to land.
func TestFramesDecodeThroughBrowserRescale(t *testing.T) {
	if testing.Short() {
		t.Skip("decodes dozens of images; skipped under -short")
	}
	const frames = 4
	sizes := []int{480, 520, 600, 700, 800, 900}

	good, total := 0, 0
	var misses []string
	for i := 0; i < frames; i++ {
		img, err := png.Decode(strings.NewReader(string(renderPNG(frameFor(1000+i, 2048, 36013), defaultPPM))))
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		for _, size := range sizes {
			region := captureRegion(smoothRescale(img, size, size), 40)
			raw, _, _ := qr.DecodeRawMultiScaleVerbose(region, []float64{1.0, 0.6, 1.7})
			total++
			if raw != nil && qr.ExtractFramePayload(raw) != nil {
				good++
				continue
			}
			misses = append(misses, fmt.Sprintf("frame %d @ %dpx", i, size))
		}
	}
	rate := float64(good) / float64(total)
	if rate < 0.8 {
		t.Errorf("only %.0f%% of frame/window combinations decoded (%d/%d); misses: %v",
			rate*100, good, total, misses)
	}
	t.Logf("decode rate %.0f%% (%d/%d) across %d window sizes", rate*100, good, total, len(sizes))
}

// TestFrameImageIsAnExactModuleMultiple pins the geometry the 1:1 display
// depends on: the image must be exactly modules*ppm pixels, so the page can show
// it at that size and have every module land on whole device pixels. The old
// fixed 512 px render put a 169-module symbol at 2 px/module inside a 354 px
// area with 79 px of white on each side — a fifth of the module density the
// display size implied.
func TestFrameImageIsAnExactModuleMultiple(t *testing.T) {
	frame := frameFor(7, 2048, 36013)
	mods := qr.ModulesForBytes(len(frame))
	for ppm := minPPM; ppm <= maxPPM; ppm++ {
		img, err := png.Decode(strings.NewReader(string(renderPNG(frame, ppm))))
		if err != nil {
			t.Fatalf("ppm %d: %v", ppm, err)
		}
		if got, want := img.Bounds().Dx(), mods*ppm; got != want {
			t.Errorf("ppm %d: image %d px, want %d (%d modules)", ppm, got, want, mods)
		}
	}
}

// TestPickPPM checks the choice the page's fit budget turns into: the largest
// whole module size that still fits, clamped to a range that decodes.
func TestPickPPM(t *testing.T) {
	cases := []struct{ fit, modules, want int }{
		{0, 177, defaultPPM}, // no budget: default
		{800, 177, 4},        // 4.5 → 4
		{885, 177, 5},        // exactly 5
		{200, 177, 2},        // clamped up to the minimum
		{4000, 177, maxPPM},  // clamped down
		{800, 0, defaultPPM}, // unknown geometry
	}
	for _, c := range cases {
		if got := pickPPM(c.fit, c.modules); got != c.want {
			t.Errorf("pickPPM(%d, %d) = %d, want %d", c.fit, c.modules, got, c.want)
		}
	}
}

// TestEncodeReportsModuleCount guards the number the page needs in order to size
// the frame image to whole device pixels per module. Without it the page falls
// back to viewport scaling, which is the behaviour that made the same setup
// decode or fail depending on the window width.
func TestEncodeReportsModuleCount(t *testing.T) {
	srv := startTestServer(t)
	body := bytes.Repeat([]byte("qrt-pro module count "), 400) // ~9 KB

	for _, tc := range []struct {
		name string
		path string
		want int
	}{
		{"classic 2048", "/encode?split=2048&codec=raptorq", 181},
		{"classic 1024", "/encode?split=1024&codec=raptorq", 137},
		{"color", "/encode?split=1200&codec=raptorq&mode=color", qr.ColorMatrixSize},
	} {
		resp, err := http.Post(srv.url(tc.path), "application/octet-stream", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var out struct {
			Modules int `json:"modules"`
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("%s: decode response: %v", tc.name, err)
		}
		if out.Modules != tc.want {
			t.Errorf("%s: modules = %d, want %d", tc.name, out.Modules, tc.want)
		}
	}
}

// TestDecimenModuleCount pins the pinned-version geometry.
func TestDecimenModuleCount(t *testing.T) {
	if got := frameModules("decimen", nil); got != 185 {
		t.Errorf("decimen modules = %d, want 185 (V40 + quiet zone)", got)
	}
}

// TestFrameImagesAreNotStretchedByCSS: applyFrameSize sets an explicit pixel
// size on every frame image. A `width:100%` rule would stretch it again and
// reintroduce fractional module sizes, which is what the sizing prevents.
func TestFrameImagesAreNotStretchedByCSS(t *testing.T) {
	page := string(indexHTML)
	if !strings.Contains(page, "applyFrameSize") {
		t.Fatal("index.html no longer sizes frames from modules*ppm")
	}
	if strings.Contains(page, "#grid img { width:100%") {
		t.Error("`#grid img` is stretched to the cell again; let applyFrameSize set the size")
	}
	if !strings.Contains(page, "fit=") {
		t.Error("the page stopped telling the server how much room it has, so ppm falls back to the default")
	}
}

// TestFramesDecodeAtIntegerModuleSizes pins the guarantee fitFrames() relies on:
// when the image is shown at exactly ppm*modules pixels (no scaling at all),
// every integer module size from 2 to 8 decodes, with or without margin around
// it. Measured before writing it: 21 of 21 frame/size/margin combinations
// decoded, versus ~60% at fractional sizes. The page therefore never has to
// search for a window size — it only has to land on an integer.
func TestFramesDecodeAtIntegerModuleSizes(t *testing.T) {
	if testing.Short() {
		t.Skip("decodes 21 images; skipped under -short")
	}
	const chunk = 2048
	mods := qr.ModulesForBytes(chunk + 25) // + the frame header
	for ppm := 2; ppm <= 8; ppm++ {
		for _, margin := range []int{0, 10, 40} {
			img, err := qr.Encode(string(frameFor(1000+ppm, chunk, 36013)), -ppm, qr.Medium)
			if err != nil {
				t.Fatalf("ppm %d: %v", ppm, err)
			}
			if got := img.Bounds().Dx(); got != ppm*mods {
				t.Fatalf("ppm %d: image %d px, want %d", ppm, got, ppm*mods)
			}
			raw, _, _ := qr.DecodeRawMultiScaleVerbose(captureRegion(img, margin), []float64{1.0, 0.6, 1.7})
			if raw == nil || qr.ExtractFramePayload(raw) == nil {
				t.Errorf("ppm %d margin %d: frame did not decode at 1:1", ppm, margin)
			}
		}
	}
}

// TestExplicitPPMOverridesFit covers the "manual" scaling mode: the number the
// user picks must be what the frames are rendered at, not a suggestion.
func TestExplicitPPMOverridesFit(t *testing.T) {
	srv := startTestServer(t)
	body := bytes.Repeat([]byte("qrt-pro manual ppm "), 300)

	for _, tc := range []struct {
		query   string
		wantPPM int
	}{
		{"&ppm=3", 3},
		{"&ppm=6&fit=200", 6},   // explicit wins over the budget
		{"&ppm=99", maxPPM},     // clamped
		{"&ppm=1", minPPM},      // below the usable range
		{"&ppm=abc&fit=800", 4}, // junk explicit value falls back to fit
	} {
		resp, err := http.Post(srv.url("/encode?split=2048&codec=raptorq"+tc.query),
			"application/octet-stream", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("%s: %v", tc.query, err)
		}
		var out struct {
			Modules int    `json:"modules"`
			PPM     int    `json:"ppm"`
			ID      string `json:"id"`
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("%s: decode response: %v", tc.query, err)
		}
		if out.PPM != tc.wantPPM {
			t.Errorf("%s: ppm = %d, want %d", tc.query, out.PPM, tc.wantPPM)
		}

		// The rendered frame must be exactly modules*ppm, which is what makes the
		// page's 1:1 display work.
		fr, err := http.Get(srv.url("/frame/" + out.ID + "/0"))
		if err != nil {
			t.Fatalf("%s: frame: %v", tc.query, err)
		}
		img, err := png.Decode(fr.Body)
		fr.Body.Close()
		srv.mu.Lock()
		delete(srv.sessions, out.ID)
		srv.mu.Unlock()
		if err != nil {
			t.Fatalf("%s: decode png: %v", tc.query, err)
		}
		if want := out.Modules * out.PPM; img.Bounds().Dx() != want {
			t.Errorf("%s: frame is %d px, want %d (modules*ppm)", tc.query, img.Bounds().Dx(), want)
		}
	}
}

// TestFrameSizeForModules pins the resolution order: ppm, then fit, then default.
func TestFrameSizeForModules(t *testing.T) {
	cases := []struct {
		size    frameSize
		modules int
		want    int
	}{
		{frameSize{ppm: 5}, 177, 5},
		{frameSize{ppm: 5, fit: 200}, 177, 5}, // explicit wins
		{frameSize{ppm: 99}, 177, maxPPM},     // clamped
		{frameSize{fit: 800}, 177, 4},         // derived from the budget
		{frameSize{}, 177, defaultPPM},        // nothing given
	}
	for _, c := range cases {
		if got := c.size.forModules(c.modules); got != c.want {
			t.Errorf("%+v for %d modules = %d, want %d", c.size, c.modules, got, c.want)
		}
	}
}

// TestPageExposesScalingModes guards the switch itself: three modes, the manual
// size input, and the query parameters each one sends.
func TestPageExposesScalingModes(t *testing.T) {
	page := string(indexHTML)
	for _, want := range []string{
		`id="fitMode"`,
		`value="fixed"`,
		`value="scale"`,
		`value="manual"`,
		`id="ppmInput"`,
		"'&ppm='",
		"'&fit='",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("index.html is missing %s", want)
		}
	}
}
