package vipsprocessor

/*
#cgo linux LDFLAGS: -ldl
#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdlib.h>

// LibRaw is looked up at run time in the process, where libvips puts it when it is built with RAW support. Building
// imagor needs no LibRaw, and nothing of LibRaw is relied on but functions of its C API and the layout of
// libraw_processed_image_t, which has not changed since LibRaw 0.9.
typedef struct {
	int type;
	unsigned short height, width, colors, bits;
	unsigned int data_size;
	unsigned char data[1];
} imagor_raw_image;

#define IMAGOR_RAW_IMAGE_JPEG 1
#define IMAGOR_RAW_NONEXISTENT_THUMBNAIL -9

typedef void *(*imagor_raw_init_fn)(unsigned int);
typedef int (*imagor_raw_open_buffer_fn)(void *, const void *, size_t);
typedef int (*imagor_raw_unpack_thumb_fn)(void *);
typedef int (*imagor_raw_unpack_thumb_ex_fn)(void *, int);
typedef imagor_raw_image *(*imagor_raw_make_mem_thumb_fn)(void *, int *);
typedef void (*imagor_raw_clear_mem_fn)(imagor_raw_image *);
typedef void (*imagor_raw_close_fn)(void *);

static imagor_raw_init_fn imagor_raw_init;
static imagor_raw_open_buffer_fn imagor_raw_open_buffer;
static imagor_raw_unpack_thumb_fn imagor_raw_unpack_thumb;
static imagor_raw_unpack_thumb_ex_fn imagor_raw_unpack_thumb_ex;
static imagor_raw_make_mem_thumb_fn imagor_raw_make_mem_thumb;
static imagor_raw_clear_mem_fn imagor_raw_clear_mem;
static imagor_raw_close_fn imagor_raw_close;

static int imagor_raw_load(void) {
	imagor_raw_init = (imagor_raw_init_fn)dlsym(RTLD_DEFAULT, "libraw_init");
	imagor_raw_open_buffer = (imagor_raw_open_buffer_fn)dlsym(RTLD_DEFAULT, "libraw_open_buffer");
	imagor_raw_unpack_thumb = (imagor_raw_unpack_thumb_fn)dlsym(RTLD_DEFAULT, "libraw_unpack_thumb");
	// LibRaw 0.21 and later
	imagor_raw_unpack_thumb_ex = (imagor_raw_unpack_thumb_ex_fn)dlsym(RTLD_DEFAULT, "libraw_unpack_thumb_ex");
	imagor_raw_make_mem_thumb = (imagor_raw_make_mem_thumb_fn)dlsym(RTLD_DEFAULT, "libraw_dcraw_make_mem_thumb");
	imagor_raw_clear_mem = (imagor_raw_clear_mem_fn)dlsym(RTLD_DEFAULT, "libraw_dcraw_clear_mem");
	imagor_raw_close = (imagor_raw_close_fn)dlsym(RTLD_DEFAULT, "libraw_close");
	return imagor_raw_init && imagor_raw_open_buffer && imagor_raw_unpack_thumb && imagor_raw_make_mem_thumb &&
		imagor_raw_clear_mem && imagor_raw_close;
}

// LibRaw reads buf for as long as the handle is open.
static void *imagor_raw_open(const void *buf, size_t len) {
	void *raw = imagor_raw_init(0);
	if (raw == NULL) {
		return NULL;
	}
	if (imagor_raw_open_buffer(raw, buf, len) != 0) {
		imagor_raw_close(raw);
		return NULL;
	}
	return raw;
}

// imagor_raw_preview unpacks the preview at index, or LibRaw's default preview for a negative index. It returns 1
// with the preview in out, which imagor_raw_free releases, 0 when the preview is not a JPEG, and -1 when there is
// no preview at index.
static int imagor_raw_preview(void *raw, int index, imagor_raw_image **out) {
	*out = NULL;
	int ret;
	if (index < 0) {
		ret = imagor_raw_unpack_thumb(raw);
	} else if (imagor_raw_unpack_thumb_ex != NULL) {
		ret = imagor_raw_unpack_thumb_ex(raw, index);
	} else {
		return -1;
	}
	if (ret == IMAGOR_RAW_NONEXISTENT_THUMBNAIL || (index < 0 && ret != 0)) {
		return -1;
	}
	if (ret != 0) {
		return 0;
	}
	int err = 0;
	imagor_raw_image *image = imagor_raw_make_mem_thumb(raw, &err);
	if (image == NULL) {
		return 0;
	}
	if (image->type != IMAGOR_RAW_IMAGE_JPEG) {
		imagor_raw_clear_mem(image);
		return 0;
	}
	*out = image;
	return 1;
}

static void imagor_raw_free(imagor_raw_image *image) {
	imagor_raw_clear_mem(image);
}

static void imagor_raw_done(void *raw) {
	imagor_raw_close(raw);
}
*/
import "C"

import (
	"errors"
	"math"
	"sync"
	"unsafe"

	"github.com/cshum/imagor"
	"github.com/cshum/vipsgen/v2/vips"
)

// maxRawPreviews bounds the previews looked at in one file, cameras store up to three.
const maxRawPreviews = 16

var errNoEmbeddedPreview = errors.New("no embedded preview fills the box")

var (
	libRawOnce   sync.Once
	libRawLoaded bool
)

// hasLibRaw reports whether the process has LibRaw, which libvips links when it is built with RAW support.
func hasLibRaw() bool {
	libRawOnce.Do(func() {
		libRawLoaded = C.imagor_raw_load() != 0
	})
	return libRawLoaded
}

// embeddedPreviewThumbnail makes the thumbnail from a preview stored in the file, when one fills the box. ok is
// false when the full image has to be decoded instead.
func (v *Processor) embeddedPreviewThumbnail(
	blob *imagor.Blob, width, height int, crop vips.Interesting, size vips.Size, n, page int,
) (img *vips.Image, ok bool) {
	if blob == nil || size == vips.SizeForce || isMultiPage(blob, n, page) {
		return nil, false
	}
	var err error
	switch {
	case blob.IsRaw() && blob.BlobType() != imagor.BlobTypeCR2 && v.hasDcrawload:
		img, err = rawPreviewThumbnail(blob, width, height, crop, size)
	default:
		return nil, false
	}
	return img, err == nil
}

// rawPreviewThumbnail makes the thumbnail from the smallest JPEG preview stored in a camera RAW file that fills the
// box, which costs a fraction of developing the raw data. With LibRaw in the process every preview is a candidate,
// up to the full size one cameras store, otherwise the one libvips picks, which is smaller than the image.
func rawPreviewThumbnail(
	blob *imagor.Blob, width, height int, crop vips.Interesting, size vips.Size,
) (*vips.Image, error) {
	buf, err := blob.ReadAll()
	if err != nil {
		return nil, err
	}
	// loading only reads the header, which carries the orientation and the preview libvips picked
	header, err := vips.NewDcrawloadBuffer(buf, nil)
	if err != nil {
		return nil, err
	}
	orientation := header.Orientation()
	previews := rawPreviews(buf)
	if len(previews) == 0 {
		if preview, err := header.GetBlob("raw-thumbnail-data"); err == nil && len(preview) > 0 {
			previews = [][]byte{preview}
		}
	}
	header.Close()
	jpeg := smallestCoveringPreview(previews, orientation, width, height, crop)
	if jpeg == nil {
		return nil, errNoEmbeddedPreview
	}
	return vips.NewThumbnailBuffer(jpeg, width, &vips.ThumbnailBufferOptions{
		Height: height, Crop: crop, Size: size,
	})
}

// rawPreviews returns the JPEG previews LibRaw finds in a camera RAW file, none without LibRaw.
func rawPreviews(buf []byte) [][]byte {
	if len(buf) == 0 || !hasLibRaw() {
		return nil
	}
	// LibRaw keeps reading the buffer it opened, which Go memory may not be
	cbuf := C.CBytes(buf)
	defer C.free(cbuf)
	raw := C.imagor_raw_open(cbuf, C.size_t(len(buf)))
	if raw == nil {
		return nil
	}
	defer C.imagor_raw_done(raw)

	var previews [][]byte
	appendPreview := func(index C.int) bool {
		var image *C.imagor_raw_image
		status := C.imagor_raw_preview(raw, index, &image)
		if status > 0 {
			previews = append(previews, C.GoBytes(unsafe.Pointer(&image.data[0]), C.int(image.data_size)))
			C.imagor_raw_free(image)
		}
		return status >= 0
	}
	for index := C.int(0); index < maxRawPreviews; index++ {
		if !appendPreview(index) {
			break
		}
	}
	if len(previews) == 0 {
		// a LibRaw without libraw_unpack_thumb_ex only offers its default preview
		appendPreview(-1)
	}
	return previews
}

// smallestCoveringPreview picks the smallest preview that fills the box once turned upright by orientation, the
// orientation of the RAW file, and returns it with that orientation set.
func smallestCoveringPreview(previews [][]byte, orientation, width, height int, crop vips.Interesting) []byte {
	var best []byte
	bestArea, bestOrientation := 0, 0
	for _, preview := range previews {
		header, err := vips.NewImageFromBuffer(preview, nil)
		if err != nil {
			continue
		}
		w, h, own := header.Width(), header.Height(), header.Orientation()
		header.Close()
		// a preview that carries its own orientation is turned by it
		upright := orientation
		if own > 1 {
			upright = own
		}
		if upright >= 5 {
			w, h = h, w
		}
		if !coversBox(w, h, width, height, crop) {
			continue
		}
		if best == nil || w*h < bestArea {
			best, bestArea = preview, w*h
			bestOrientation = 0
			if own <= 1 {
				bestOrientation = orientation
			}
		}
	}
	if bestOrientation > 1 {
		return withOrientation(best, bestOrientation)
	}
	return best
}

// withOrientation inserts an EXIF segment holding only the orientation tag right after the JPEG start marker, so
// the thumbnail turns the preview upright. Turning its sequential output afterwards fails on large previews.
func withOrientation(jpeg []byte, orientation int) []byte {
	if len(jpeg) < 2 || jpeg[0] != 0xFF || jpeg[1] != 0xD8 {
		return jpeg
	}
	exif := []byte{
		0xFF, 0xE1, 0x00, 0x22, // APP1, 34 bytes
		'E', 'x', 'i', 'f', 0x00, 0x00,
		'I', 'I', 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, // little-endian TIFF, IFD0 at 8
		0x01, 0x00, // one entry
		0x12, 0x01, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00, byte(orientation), 0x00, 0x00, 0x00, // Orientation, SHORT
		0x00, 0x00, 0x00, 0x00, // no next IFD
	}
	out := make([]byte, 0, len(jpeg)+len(exif))
	out = append(out, jpeg[:2]...)
	out = append(out, exif...)
	return append(out, jpeg[2:]...)
}

// coversBox reports whether a w x h image fills the box without being enlarged.
func coversBox(w, h, boxWidth, boxHeight int, crop vips.Interesting) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	scaleX := float64(boxWidth) / float64(w)
	scaleY := float64(boxHeight) / float64(h)
	scale := math.Min(scaleX, scaleY)
	if crop != vips.InterestingNone {
		scale = math.Max(scaleX, scaleY)
	}
	return scale <= 1
}
