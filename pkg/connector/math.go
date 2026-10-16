package connector

import (
	"context"
	"errors"
	"sync"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-zulip/pkg/msgconv"
	"go.mau.fi/mautrix-zulip/pkg/msgconv/mathrender"
	"go.mau.fi/mautrix-zulip/pkg/msgconv/zuliphtml"
)

type mathCacheKey struct {
	TeX     string
	Display bool
}

// mathImageRenderer renders math into images and uploads them, caching the uploads so that
// formulas that appear repeatedly are only uploaded once.
type mathImageRenderer struct {
	renderer *mathrender.Renderer
	mode     zuliphtml.MathMode

	cacheLock sync.Mutex
	cache     map[mathCacheKey]*zuliphtml.MathImage
}

const maxMathCacheSize = 10000

var _ msgconv.MathRenderer = (*mathImageRenderer)(nil)

func newMathImageRenderer(cfg *MathConfig, log zerolog.Logger) *mathImageRenderer {
	return &mathImageRenderer{
		renderer: mathrender.New(mathrender.Options{
			NodePath:     cfg.NodePath,
			RendererPath: cfg.RendererPath,
			Foreground:   cfg.Foreground,
			Background:   cfg.Background,
			Halo:         cfg.Halo,
		}, log.With().Str("component", "math renderer").Logger()),
		mode:  zuliphtml.MathMode(cfg.Mode),
		cache: make(map[mathCacheKey]*zuliphtml.MathImage),
	}
}

func (mir *mathImageRenderer) MathMode() zuliphtml.MathMode {
	return mir.mode
}

func (mir *mathImageRenderer) RenderMath(
	ctx context.Context, intent bridgev2.MatrixAPI, roomID id.RoomID, tex string, display bool,
) *zuliphtml.MathImage {
	key := mathCacheKey{TeX: tex, Display: display}
	mir.cacheLock.Lock()
	cached, ok := mir.cache[key]
	mir.cacheLock.Unlock()
	if ok {
		return cached
	}
	log := zerolog.Ctx(ctx).With().Str("tex", tex).Bool("display", display).Logger()
	rendered, err := mir.renderer.Render(ctx, tex, display)
	if err != nil {
		if errors.Is(err, mathrender.ErrRenderFailed) {
			log.Debug().Err(err).Msg("Failed to render math, using LaTeX source instead")
		} else {
			log.Err(err).Msg("Failed to render math")
		}
		return nil
	}
	// Images inside formatted bodies can't reference encrypted files, so upload without a room ID to skip
	// encryption even in encrypted rooms. This also lets the cached upload be reused in any room.
	mxc, err := uploadMathImage(ctx, intent, rendered.PNG)
	if err != nil || mxc == "" {
		log.Err(err).Msg("Failed to upload rendered math")
		return nil
	}
	img := &zuliphtml.MathImage{URL: mxc, Width: rendered.Width, Height: rendered.Height}
	mir.cacheLock.Lock()
	if len(mir.cache) >= maxMathCacheSize {
		clear(mir.cache)
	}
	mir.cache[key] = img
	mir.cacheLock.Unlock()
	return img
}

// uploadMathImage uploads a rendered formula and waits until the upload has finished.
// With async media enabled, the normal upload returns as soon as the MXC URI is reserved, so the message
// can reach clients before the image exists. Clients show inline images in formatted bodies as broken
// in that case and don't retry, so math must not be uploaded asynchronously.
func uploadMathImage(ctx context.Context, intent bridgev2.MatrixAPI, png []byte) (id.ContentURIString, error) {
	req := mautrix.ReqUploadMedia{ContentBytes: png, ContentType: "image/png", FileName: "math.png"}
	as, ok := intent.(*matrix.ASIntent)
	if !ok || !as.Connector.Config.Homeserver.AsyncMedia {
		mxc, _, err := intent.UploadMedia(ctx, "", req.ContentBytes, req.FileName, req.ContentType)
		return mxc, err
	}
	// Reserve the MXC URI and upload to it synchronously, like the async upload does in the background
	created, err := as.Matrix.CreateMXC(ctx)
	if err != nil {
		return "", err
	}
	req.MXC = created.ContentURI
	req.UnstableUploadURL = created.UnstableUploadURL
	if _, err = as.Matrix.UploadMedia(ctx, req); err != nil {
		return "", err
	}
	return created.ContentURI.CUString(), nil
}

// mathRenderer returns the math renderer, or nil if math rendering is disabled.
func (zc *ZulipConnector) mathRenderer() msgconv.MathRenderer {
	if zc.math == nil {
		return nil
	}
	return zc.math
}
