package mathrender

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRender(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	rendererPath, _ := filepath.Abs("../../../math-renderer/render.mjs")
	if err != nil {
		t.Skip("node not installed")
	} else if _, err = os.Stat(filepath.Join(filepath.Dir(rendererPath), "node_modules")); err != nil {
		t.Skip("math-renderer dependencies not installed")
	}
	r := New(Options{NodePath: nodePath, RendererPath: rendererPath, Foreground: "#000", Background: "#fff"}, zerolog.Nop())
	img, err := r.Render(context.Background(), `\frac{a}{b}`, true)
	require.NoError(t, err)
	assert.Equal(t, []byte("\x89PNG"), img.PNG[:4])
	assert.Greater(t, img.Width, 0)
	assert.Greater(t, img.Height, 0)

	_, err = r.Render(context.Background(), `\frac{a`, false)
	assert.True(t, errors.Is(err, ErrRenderFailed))
}
