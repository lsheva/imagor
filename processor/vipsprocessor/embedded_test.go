package vipsprocessor

import (
	"errors"
	"os"
	"testing"

	"github.com/cshum/imagor"
	"github.com/cshum/vipsgen/v2/vips"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPreviewJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img, err := vips.NewGaussnoise(width, height, nil)
	require.NoError(t, err)
	defer img.Close()
	buf, err := img.JpegsaveBuffer(nil)
	require.NoError(t, err)
	return buf
}

func jpegSize(t *testing.T, buf []byte) (width, height, orientation int) {
	t.Helper()
	img, err := vips.NewImageFromBuffer(buf, nil)
	require.NoError(t, err)
	defer img.Close()
	return img.Width(), img.Height(), img.Orientation()
}

func TestCoversBox(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		box           [2]int
		crop          vips.Interesting
		want          bool
	}{
		{"landscape fits the width exactly", 320, 213, [2]int{320, 320}, vips.InterestingNone, true},
		{"portrait fits the height exactly", 240, 320, [2]int{320, 320}, vips.InterestingNone, true},
		{"too small to fit", 240, 320, [2]int{384, 384}, vips.InterestingNone, false},
		{"fills the crop", 320, 240, [2]int{320, 180}, vips.InterestingCentre, true},
		{"too narrow to fill the crop", 240, 320, [2]int{320, 180}, vips.InterestingCentre, false},
		{"no size", 0, 0, [2]int{320, 320}, vips.InterestingNone, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, coversBox(tt.width, tt.height, tt.box[0], tt.box[1], tt.crop))
		})
	}
}

func TestWithOrientation(t *testing.T) {
	_, _, orientation := jpegSize(t, withOrientation(testPreviewJPEG(t, 64, 48), 6))
	assert.Equal(t, 6, orientation)
	assert.Equal(t, []byte("not a jpeg"), withOrientation([]byte("not a jpeg"), 6))
}

func TestSmallestCoveringPreview(t *testing.T) {
	small, medium, large := testPreviewJPEG(t, 160, 120), testPreviewJPEG(t, 640, 480), testPreviewJPEG(t, 2000, 1500)
	previews := [][]byte{large, small, medium}

	t.Run("picks the smallest that fills the box", func(t *testing.T) {
		width, _, _ := jpegSize(t, smallestCoveringPreview(previews, 1, 320, 320, vips.InterestingNone))
		assert.Equal(t, 640, width)
	})
	t.Run("picks an exact fit", func(t *testing.T) {
		width, _, _ := jpegSize(t, smallestCoveringPreview(previews, 1, 640, 640, vips.InterestingNone))
		assert.Equal(t, 640, width)
	})
	t.Run("picks a larger one for a larger box", func(t *testing.T) {
		width, _, _ := jpegSize(t, smallestCoveringPreview(previews, 1, 1000, 1000, vips.InterestingNone))
		assert.Equal(t, 2000, width)
	})
	t.Run("none fills the box", func(t *testing.T) {
		assert.Nil(t, smallestCoveringPreview(previews, 1, 3000, 3000, vips.InterestingNone))
	})
	t.Run("measures the upright preview and carries the orientation", func(t *testing.T) {
		// upright the medium preview is 480 wide, too narrow to fill a 600 wide crop
		width, _, orientation := jpegSize(t, smallestCoveringPreview(previews, 6, 600, 300, vips.InterestingCentre))
		assert.Equal(t, 2000, width)
		assert.Equal(t, 6, orientation)
	})
}

func TestHeifThumbnail(t *testing.T) {
	// a 1634x2224 image that stores a 234x320 thumbnail
	blob := imagor.NewBlobFromFile("../../testdata/gopher-thumbnail.heic")

	img, err := heifThumbnail(blob, 320, 320, vips.InterestingNone, vips.SizeDown)
	require.NoError(t, err)
	defer img.Close()
	assert.Equal(t, 234, img.Width())
	assert.Equal(t, 320, img.Height())

	_, err = heifThumbnail(blob, 384, 384, vips.InterestingNone, vips.SizeDown)
	assert.ErrorIs(t, err, errNoEmbeddedPreview)
}

// TestRawPreviewThumbnail needs a camera RAW file with embedded previews, which the repository does not carry:
// IMAGOR_TEST_RAW names one.
func TestRawPreviewThumbnail(t *testing.T) {
	path := os.Getenv("IMAGOR_TEST_RAW")
	if path == "" {
		t.Skip("IMAGOR_TEST_RAW is not set")
	}
	if !vips.HasOperation("dcrawload_buffer") {
		t.Skip("libvips has no dcrawload")
	}
	buf, err := os.ReadFile(path)
	require.NoError(t, err)
	t.Logf("LibRaw in the process: %v", hasLibRaw())
	for _, preview := range rawPreviews(buf) {
		width, height, _ := jpegSize(t, preview)
		t.Logf("preview %dx%d", width, height)
	}

	// a box no stored preview fills is left to the full image
	for _, box := range []int{320, 1280, 1920} {
		img, err := rawPreviewThumbnail(imagor.NewBlobFromBytes(buf), box, box, vips.InterestingNone, vips.SizeDown)
		if errors.Is(err, errNoEmbeddedPreview) {
			t.Logf("box %d: no preview fills it", box)
			continue
		}
		require.NoError(t, err, "box %d", box)
		assert.Equal(t, box, max(img.Width(), img.Height()), "box %d", box)
		t.Logf("box %d: %dx%d", box, img.Width(), img.Height())
		img.Close()
	}
}
